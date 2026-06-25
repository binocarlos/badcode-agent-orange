---
name: driver-analysis
description: Key driver analysis (KDA) identifying which variables most influence a target outcome, with regression, importance ranking, and stakeholder-tailored outputs
triggers:
  - key drivers
  - driver analysis
  - what drives
  - importance ranking
  - regression on
  - which attributes influence
keywords: [driver analysis, key drivers, regression, importance, correlation, shapley, relative importance, what drives, influence, predict]
---

# Driver Analysis

## When to Use
- User asks "what drives X?" or "what influences Y?"
- User wants to understand which attributes most impact a KPI (e.g., satisfaction, NPS, consideration)
- User needs to prioritise which levers to pull to improve an outcome
- User asks for importance-performance analysis or quadrant mapping

> **Requires raw case data** -- see the **casedata** skill for `pt casedata` usage.

## Two-Phase Gate

**Phase A: Data Science** -- variable selection, data extraction, model fitting, validation. Produce a technical report. Do NOT proceed to Phase B until the user confirms the model is sound.

**Phase B: Strategic Reporting** -- importance ranking, quadrant map, stakeholder outputs (webapp, PPTX, dashboard).

## Method Selection

| Situation | Method |
|-----------|--------|
| **Standard KDA** (numeric DV, numeric IVs) | Multiple linear regression + standardised coefficients |
| **Many correlated predictors** | Relative importance (Lindeman-Merenda-Gold / dominance analysis) |
| **Binary outcome** (e.g., would recommend yes/no) | Logistic regression + marginal effects |
| **Non-linear relationships suspected** | Random forest feature importance or SHAP values |
| **Small sample or many predictors** | Ridge/Lasso regression (regularised) |

**Default recommendation:** Start with multiple linear regression. If predictors are highly correlated (VIF > 5), switch to relative importance analysis.

## Phase A: Data Science

### 1. Discovery

```
pt search "satisfaction"
pt search "overall"
pt vars
```

Use `ask_user` to confirm:
- **Dependent variable** (the outcome to explain, e.g., overall satisfaction, NPS, likelihood to recommend)
- **Independent variables** (potential drivers, e.g., attribute ratings, service dimensions)
- **Business objective** (what decisions will this inform?)

**Common patterns:**
- Overall satisfaction driven by attribute satisfaction scores
- NPS driven by experience ratings
- Brand consideration driven by brand perception attributes
- Purchase intent driven by product features

### 2. Extraction

```
pt casedata OverallSat Attr1 Attr2 Attr3 Attr4 Attr5
```

```python
import sys; sys.path.insert(0, '/workspace/lib')
from casedata import to_dataframe
import pandas as pd

df = to_dataframe('/workspace/data/casedata_overallsat.json',
                  '/workspace/data/casedata_attr1.json',
                  '/workspace/data/casedata_attr2.json',
                  labeled=False)  # Raw numeric codes
```

**Handle missing data:**

```python
# Identify DK/NA codes (typically 99, 98, or variable-specific)
for col in df.columns:
    print(f"{col}: unique values = {sorted(df[col].dropna().unique())}")

# Replace DK codes with NaN
dk_codes = [99, 98]
df = df.replace(dk_codes, pd.NA)

# Report completeness
complete = df.dropna()
excluded = len(df) - len(complete)
print(f"Total: {len(df)}, Complete cases: {len(complete)}, Excluded: {excluded} ({excluded/len(df)*100:.1f}%)")
```

**Checkpoint:** Report exclusion rate via `ask_user`. If >15%, discuss listwise vs pairwise deletion or imputation.

### 3. Diagnostics

```python
import numpy as np
from scipy.stats import pearsonr

df_clean = df.dropna()
dv = df_clean.columns[0]  # Dependent variable
ivs = df_clean.columns[1:]  # Independent variables

# Correlation matrix
corr = df_clean[ivs].corr()
print("Predictor correlations (watch for >0.7):")
for i, v1 in enumerate(ivs):
    for v2 in ivs[i+1:]:
        r = corr.loc[v1, v2]
        if abs(r) > 0.5:
            print(f"  {v1} x {v2}: r={r:.3f} {'*** HIGH' if abs(r) > 0.7 else ''}")

# DV correlations (bivariate importance)
print(f"\nBivariate correlations with {dv}:")
for iv in ivs:
    r, p = pearsonr(df_clean[dv], df_clean[iv])
    print(f"  {iv}: r={r:.3f} (p={p:.4f})")
```

**Decision point:** If any predictor pair has r > 0.7, note multicollinearity. Consider:
1. Combining correlated variables (factor analysis first)
2. Dropping one of the pair
3. Using relative importance analysis instead of raw coefficients

### 4. Model Fitting

**Standard regression:**

```python
import statsmodels.api as sm

X = df_clean[ivs].astype(float)
y = df_clean[dv].astype(float)
X_const = sm.add_constant(X)

model = sm.OLS(y, X_const).fit()
print(model.summary())

# Standardised coefficients (the key output)
from sklearn.preprocessing import StandardScaler
scaler = StandardScaler()
X_std = pd.DataFrame(scaler.fit_transform(X), columns=ivs)
y_std = (y - y.mean()) / y.std()

model_std = sm.OLS(y_std, sm.add_constant(X_std)).fit()
importance = model_std.params[1:].abs().sort_values(ascending=False)
print("\nStandardised importance ranking:")
for var, coef in importance.items():
    direction = "+" if model_std.params[var] > 0 else "-"
    print(f"  {var}: {coef:.3f} ({direction})")
```

**VIF check:**

```python
from statsmodels.stats.outliers_influence import variance_inflation_factor

vif = pd.DataFrame({
    'Variable': ivs,
    'VIF': [variance_inflation_factor(X_const.values, i+1) for i in range(len(ivs))]
})
print(vif.sort_values('VIF', ascending=False))
# VIF > 5 = concerning, VIF > 10 = serious multicollinearity
```

**If VIF > 5, use relative importance:**

```python
# Relative importance (simplified dominance analysis)
# Calculates each predictor's contribution to R-squared
from itertools import combinations

def relative_importance(X, y):
    n_vars = X.shape[1]
    var_names = X.columns.tolist()
    contributions = {v: 0.0 for v in var_names}
    
    for size in range(1, n_vars + 1):
        for combo in combinations(range(n_vars), size):
            cols = [var_names[i] for i in combo]
            r2 = sm.OLS(y, sm.add_constant(X[cols])).fit().rsquared
            for i in combo:
                # Average marginal contribution
                if size == 1:
                    contributions[var_names[i]] += r2
                else:
                    subset = [var_names[j] for j in combo if j != i]
                    r2_without = sm.OLS(y, sm.add_constant(X[subset])).fit().rsquared
                    contributions[var_names[i]] += (r2 - r2_without)
    
    # Normalise
    total = sum(contributions.values())
    return {k: v/total for k, v in contributions.items()}

ri = relative_importance(X, y)
print("Relative importance (% of explained variance):")
for var, imp in sorted(ri.items(), key=lambda x: -x[1]):
    print(f"  {var}: {imp*100:.1f}%")
```

### 5. Validation Report

Save as a file artifact (`driver_analysis_validation.md`):
- Model type and justification
- R-squared and adjusted R-squared
- F-statistic and p-value
- Standardised coefficients with significance
- VIF values (flag any > 5)
- Residual diagnostics (normality, heteroscedasticity)
- Importance ranking table
- Limitations and caveats

Register: `register_artifact(file_path="driver_analysis_validation.md", label="Driver Analysis - Technical Report", artifact_type="file")`

**HARD GATE:** Present report to user. Do NOT proceed to Phase B without explicit confirmation.

## Phase B: Strategic Reporting

### 6. Importance-Performance Quadrant

The quadrant map is the key strategic output. It plots:
- **X-axis:** Performance (mean score of each attribute)
- **Y-axis:** Importance (standardised coefficient or relative importance)

```python
# Calculate performance (mean scores)
performance = X.mean()

# Build quadrant data
quadrant_data = pd.DataFrame({
    'Variable': ivs,
    'Performance': performance,
    'Importance': [importance[v] for v in ivs],
    'Label': ivs  # Replace with human-readable labels
})

# Quadrant thresholds (median split)
perf_mid = quadrant_data['Performance'].median()
imp_mid = quadrant_data['Importance'].median()
```

**Quadrant labels:**
| Quadrant | Importance | Performance | Action |
|----------|-----------|-------------|--------|
| **Protect** | High | High | Maintain investment, key strengths |
| **Prioritise** | High | Low | Biggest opportunity for improvement |
| **Monitor** | Low | Low | Watch but don't over-invest |
| **Maintain** | Low | High | Performing well without heavy lift |

### 7. Stakeholder Outputs

Use `ask_user` to choose output format:
- **Interactive webapp** -- D3 scatter plot with hover tooltips, quadrant shading, filterable by subgroup
- **PowerPoint report** -- quadrant chart + importance bar chart + key findings
- **Platinum dashboard** -- embedded in the platform

**Webapp structure:**
1. Hero: headline finding ("X is the #1 driver of Y, but underperforms")
2. Quadrant scatter plot (interactive, labelled points)
3. Importance bar chart (horizontal, sorted descending)
4. Performance comparison table
5. Subgroup comparison (if applicable)
6. Methodology appendix

**For PPTX:** See **pptx-strategic-report** skill. Key slides:
- Title slide with research context
- Importance ranking (horizontal bar chart)
- Quadrant map (scatter chart)
- Top 3 drivers deep-dive (one slide each)
- Strategic recommendations

### 8. Subgroup Analysis (Optional)

If the user wants to compare drivers across segments:

```python
# Split by a grouping variable
groups = df_clean.groupby('Segment')
for name, group in groups:
    X_g = group[ivs].astype(float)
    y_g = group[dv].astype(float)
    model_g = sm.OLS(y_g, sm.add_constant(X_g)).fit()
    print(f"\n--- {name} (n={len(group)}) R2={model_g.rsquared:.3f} ---")
```

This reveals whether different segments have different drivers -- powerful for targeted strategy.

## Rules

1. Always validate with bivariate correlations before multivariate regression
2. Always check VIF -- if > 5 for any predictor, switch to relative importance
3. Never present unstandardised coefficients as importance -- always standardise
4. Always include R-squared -- if < 0.1, the model explains very little and caveats are essential
5. Always produce a technical validation report before strategic outputs
6. Do NOT proceed from Phase A to Phase B without explicit user confirmation
7. Label quadrants with actionable names (Protect/Prioritise/Monitor/Maintain), not numbers
8. Performance means come from the raw data, not percentages
9. Use `ask_user` at every checkpoint
10. Negative drivers are still important -- flag them clearly in outputs
11. Technical statistics (p-values, VIF, R-squared) go in the appendix, not headlines
12. For stakeholder-facing outputs, translate variable names to human-readable labels
