// Command oranged runs the Agent Orange orchestrator as a service — the
// "use it in anger" daemon. A human sets a top-level goal (POST /api/goal or
// the web UI goal box); a ticker runs the manager exchange on a cadence
// (plan → spawn workers → verify → the approval gate); a slower consultant
// cadence reviews the telemetry evidence and revises the manager's standing
// guidance through the board. Real Anthropic models throughout; state persists
// in a local sqlite file; approved posts land in published.jsonl behind the
// real approval gate.
//
//	ANTHROPIC_API_KEY=sk-ant-... TICK_INTERVAL=30s go run ./cmd/oranged
//	open http://localhost:8099
//
// See config.go for the full env table.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/pgstore"
	"github.com/binocarlos/badcode-agent-orange/orchestrator/watchapi"
)

func main() {
	cfg, err := resolveConfig(os.Getenv, os.ReadFile)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	// sqlite via the pgstore seam: AutoMigrate the row models directly — NOT
	// agentdb.Open, whose raw-SQL migrations are Postgres-only.
	db, err := gorm.Open(sqlite.Open(filepath.Join(cfg.DataDir, "oranged.db")), &gorm.Config{
		// record-not-found is an expected read on a fresh board (head lookup);
		// gorm's default logger would print it as an error on every boot.
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags),
			logger.Config{LogLevel: logger.Error, IgnoreRecordNotFoundError: true}),
	})
	if err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&agentdb.BoardRevision{}, &agentdb.BoardHead{}, &agentdb.BoardPromptFragment{},
		&agentdb.Ticket{}, &agentdb.TelemetryRun{},
	); err != nil {
		return err
	}
	board := pgstore.NewPgBoard(db)
	tickets := pgstore.NewPgTicketStore(db)
	tel := pgstore.NewPgTelemetry(db)

	// Idempotent guidance seed: Compose fails loud on a missing fragment, so
	// routing-guidance must exist before the first tick. Never reseed — the
	// consultant's and humans' edits are the accumulated learning.
	if !fragmentPresent(ctx, board, orchestrator.RoutingFragmentID) {
		if _, err := orchestrator.WriteFragment(ctx, board,
			orchestrator.RoutingFragmentID, cfg.SeedGuidance, "human", "seed routing guidance"); err != nil {
			return err
		}
		log.Printf("seeded %s", orchestrator.RoutingFragmentID)
	}

	// The spend meter only meters the api backend; on the subscription-backed
	// CLI it stays at 0 (the subscription's own usage limits are the ceiling).
	meter := orchestrator.NewMemSpendMeter(cfg.SpendCeilingUSD)
	var router orchestrator.ModelRouter
	switch cfg.Backend {
	case backendCLI:
		router = orchestrator.NewTierRouter(map[orchestrator.ModelTier]orchestrator.Model{
			orchestrator.TierFull:  cliModel{bin: cfg.CLIBin, model: cfg.CLIModelFull, timeout: cfg.CLITimeout},
			orchestrator.TierMid:   cliModel{bin: cfg.CLIBin, model: cfg.CLIModelMid, timeout: cfg.CLITimeout},
			orchestrator.TierCheap: cliModel{bin: cfg.CLIBin, model: cfg.CLIModelCheap, timeout: cfg.CLITimeout},
		})
		log.Printf("model backend: claude CLI (%s) on your subscription — full=%s mid=%s cheap=%s",
			cfg.CLIBin, cfg.CLIModelFull, cfg.CLIModelMid, cfg.CLIModelCheap)
	default:
		router = orchestrator.NewAnthropicRouter(orchestrator.RouterConfig{
			APIKey: cfg.APIKey, MaxTokens: cfg.MaxTokens, Meter: meter,
		})
		log.Printf("model backend: anthropic api (spend ceiling $%.2f)", cfg.SpendCeilingUSD)
	}
	ledger := orchestrator.NewSpawnLedger()
	runtime := &orchestrator.InProcRuntime{
		Board: board, Router: router,
		Sink: &orchestrator.TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: tel,
	}
	ex := &orchestrator.ManagerExchange{
		Board: board, Tickets: tickets, Router: router, Runtime: runtime,
		Ledger: ledger, Telemetry: tel,
		ProjectID: "oranged", ManagerSession: "oranged-manager",
		PlanTier: orchestrator.TierFull, WorkerTier: orchestrator.TierMid, VerifyTier: orchestrator.TierFull,
		WorkerBudget:   orchestrator.Budget{MaxDepth: 3, MaxSpawns: 8, TreeTokens: 2_000_000},
		PlanTemplate:   cfg.PlanTemplate,
		WorkerTemplate: cfg.WorkerTemplate,
		Channel:        cfg.Channel, DefaultDisposition: orchestrator.DispositionPublish,
	}

	connector := &fileConnector{path: filepath.Join(cfg.DataDir, "published.jsonl")}
	approval := orchestrator.NewApprovalService(tickets, connector, tel)
	feedback := orchestrator.HumanFeedbackApplier{Board: board, Reviser: router.For(orchestrator.TierMid)}

	d, err := NewDaemon(ex, board, tel, meter, consultantScope{
		Board: board, Model: router.For(orchestrator.TierFull),
		Tickets: tickets, Tel: tel, Charter: cfg.ConsultantCharter,
	}, cfg.TickInterval, cfg.ConsultantInterval)
	if err != nil {
		return err
	}
	if err := d.recoverStranded(ctx); err != nil {
		return err
	}
	if err := d.loadGoal(ctx, cfg.Goal); err != nil {
		return err
	}

	watch, err := watchapi.New(watchapi.Config{
		Board: board, Revisions: board, Tickets: tickets, Telemetry: tel,
		Approver: approval, Rejecter: approval, Answerer: approval,
		Feedback: feedback, Trigger: d, AuthToken: cfg.AuthToken,
	})
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: newRootMux(d, watch.Mux(), cfg.AuthToken)}
	go d.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("oranged: http://localhost%s — tick every %s, consultant every %s, data in %s",
		cfg.Addr, cfg.TickInterval, cfg.ConsultantInterval, cfg.DataDir)
	return srv.ListenAndServe()
}

// fragmentPresent reports whether the folded head board contains id; a
// zero-revision board (Current errors) contains nothing yet.
func fragmentPresent(ctx context.Context, board agentdb.BoardStore, id string) bool {
	cur, err := board.Current(ctx)
	if err != nil {
		return false
	}
	for _, f := range cur.Fragments {
		if f.ID == id {
			return true
		}
	}
	return false
}
