package main

// dbconnect.go — what agentd says when it cannot open DATABASE_URL.
//
// The failure this exists for is not exotic; it is the default first-run
// accident. docker-compose.yml renders DATABASE_URL from POSTGRES_USER /
// POSTGRES_PASSWORD / POSTGRES_DB (all defaulting to the literal
// `agentorange`), and the `pg-data` volume initialises its role and database
// exactly ONCE, on the first `docker compose up`. Setting a password in `.env`
// afterwards re-renders the connection string but does not touch the database
// that was already created — so agentd dies with raw gorm/pgx text
// ("password authentication failed for user ...") that names neither the
// `postgres` compose service nor the stale volume, and the operator has no way
// to know that the fix is `docker compose down -v`.
//
// So: keep gorm's own text (it is the fact), and wrap it with what to check.
// Same boot-validation discipline as portrange.go and gc.go — the diagnostic is
// pure, unit-tested, and does no I/O. The DSN is echoed with its password
// redacted, because it is otherwise the first thing an operator wants to see
// and the last thing that should land in a log.

import (
	"fmt"
	"net/url"
	"strings"
)

// databaseConnectError wraps a failed agentdb.Open with the two things that
// actually go wrong, named. dbURL is echoed redacted; err is preserved verbatim
// (and remains unwrappable) so nothing is lost. A nil err passes through as nil
// so the call site stays one line.
func databaseConnectError(dbURL string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("cannot open DATABASE_URL (%s): %w\n%s",
		redactDBURL(dbURL), err, dbConnectChecklist(err))
}

// dbConnectChecklist is the operator-facing "what to check", ordered by which
// cause is likelier given the error text.
func dbConnectChecklist(err error) string {
	var b strings.Builder
	b.WriteString("  check:\n")

	txt := strings.ToLower(fmt.Sprint(err))
	authLikely := strings.Contains(txt, "authentication") ||
		strings.Contains(txt, "password") ||
		strings.Contains(txt, "role \"") ||
		strings.Contains(txt, "does not exist")

	if authLikely {
		b.WriteString("  - the `pg-data` volume initialises POSTGRES_USER/POSTGRES_PASSWORD/POSTGRES_DB\n" +
			"    ONCE, on the first `docker compose up`. Changing them in .env afterwards\n" +
			"    re-renders DATABASE_URL but NOT the database. To adopt new credentials:\n" +
			"    `docker compose down -v` (this DELETES the database), then `up` again.\n")
		b.WriteString("  - POSTGRES_USER / POSTGRES_PASSWORD / POSTGRES_DB in .env match what the\n" +
			"    `postgres` service was first created with (all three default to `agentorange`).\n")
	} else {
		b.WriteString("  - the `postgres` compose service is up and healthy: `docker compose ps postgres`,\n" +
			"    `docker compose logs postgres`.\n")
		b.WriteString("  - the host/port in DATABASE_URL is reachable from this container (inside the\n" +
			"    stack it is the service name `postgres:5432`, not `localhost`).\n")
		b.WriteString("  - POSTGRES_USER / POSTGRES_PASSWORD / POSTGRES_DB in .env match what the\n" +
			"    `postgres` service was first created with; if you changed them after the first\n" +
			"    `up`, the `pg-data` volume still holds the old ones — `docker compose down -v`\n" +
			"    (which DELETES the database) is the only way to re-initialise them.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// redactDBURL replaces the password in a postgres DSN with "xxxxx". A DSN that
// does not parse is reported as "<unparseable>" rather than echoed, since the
// only thing we can be sure of about it is that it may contain a secret.
func redactDBURL(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil || u.Host == "" {
		return "<unparseable>"
	}
	// url.URL.Redacted() replaces any password with the literal "xxxxx" and
	// leaves everything else — host, database, query — intact.
	return u.Redacted()
}
