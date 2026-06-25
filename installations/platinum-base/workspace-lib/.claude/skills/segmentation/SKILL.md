---
name: segmentation
description: Respondent segmentation and clustering analysis using factor analysis, K-means, and LCA
triggers:
  - segmentation
  - clustering
  - k-means
  - what types of people are in my data
  - persona
  - latent class analysis
keywords: [segmentation, clustering, k-means, factor analysis, LCA, persona, segments, profiling, latent class]
---

# Segmentation Analysis

## When to Use
- User asks for segmentation, clustering, or persona creation
- User wants to identify groups/segments in their respondent data
- User asks "what types of people are in my data?"

> **Requires raw case data** — see the **casedata** skill for `pt casedata` usage.

## Two-Phase Gate

**Phase A: Data Science** — discovery, extraction, clustering, validation. Produce a validation report. Do NOT proceed to Phase B until the user confirms the segmentation is valid.

**Phase B: Strategic Reporting** — profiling, naming, persona visualisation, write-back.

## Method Selection

| Input Data | Method |
|------------|--------|
| **Numeric scales** (ratings, Likert) | Factor Analysis → K-means |
| **Many correlated items** | Factor Analysis first (reduce dimensions) → K-means |
| **Few uncorrelated items** | K-means or Hierarchical (Ward's) directly |
| **Categorical/binary** | Dummy-encode → K-means, or K-medoids (PAM) for outlier robustness |
| **Mixed numeric + categorical** | Factor scores from numerics + recoded categoricals → K-means on combined |

## Phase A: Data Science

### 1. Discovery

```
pt search "satisfaction"
pt search "attitude"
pt vars
```

Use `ask_user` to confirm: which variables to include, business objective, variables to exclude. Demographics are for profiling, not clustering.

### 2. Extraction

```
pt casedata Satisfaction1 Satisfaction2 Attitude1 Attitude2
```

```python
import sys; sys.path.insert(0, '/workspace/lib')
from casedata import to_dataframe
import pandas as pd

df = to_dataframe('/workspace/data/casedata_satisfaction1.json',
                  '/workspace/data/casedata_satisfaction2.json',
                  labeled=False)  # Raw codes for clustering
```

**Handle "Don't Know" codes (typically code 99):**

```python
# Count DK prevalence per variable
for col in df.columns:
    dk_count = (df[col] == 99).sum()
    print(f"{col}: {dk_count} DK responses ({dk_count/len(df)*100:.1f}%)")

# Exclude DK cases from clustering (keep track for later)
dk_mask = (df == 99).any(axis=1)
df_clean = df[~dk_mask].dropna()
excluded = len(df) - len(df_clean)
print(f"Total: {len(df)}, Usable: {len(df_clean)}, Excluded: {excluded} ({excluded/len(df)*100:.1f}%)")
```

**Checkpoint:** Report exclusion rate via `ask_user`. If >20%, discuss imputation or variable adjustment.

### 3. Clustering

```python
from sklearn.preprocessing import StandardScaler
from sklearn.decomposition import FactorAnalysis
from sklearn.cluster import KMeans
from sklearn.metrics import silhouette_score

scaler = StandardScaler()
X = scaler.fit_transform(df_clean)

# KMO test for factor analysis suitability (need KMO > 0.6)
# Factor analysis if >5 correlated input variables
fa = FactorAnalysis(n_components=min(5, X.shape[1] - 1), random_state=42)
factors = fa.fit_transform(X)

# Test K=2 through K=7
for k in range(2, 8):
    km = KMeans(n_clusters=k, random_state=42, n_init=10)
    labels = km.fit_predict(factors)
    sil = silhouette_score(factors, labels)
    print(f"K={k}: silhouette={sil:.3f}")
```

**Note:** Weak silhouette scores (0.2-0.3) are **normal** for attitudinal survey data — responses form a continuum, not discrete clusters. This doesn't invalidate the segmentation.

Use `ask_user` to confirm cluster count.

### 4. Validation Report

Save as a file artifact (`segmentation_validation_report.md`):
- Factor loadings + variance explained per factor
- Kaiser criterion (eigenvalues > 1)
- Silhouette scores for each K tested
- Cluster size distribution (flag any <5%)
- Mean input variable values per cluster
- Interpretation: >0.5 strong, 0.25-0.5 reasonable, <0.25 weak
- Limitations and caveats

Register: `register_artifact(file_path="segmentation_validation_report.md", label="Validation Report", artifact_type="file")`

**HARD GATE:** Present report to user. Do NOT proceed to Phase B without explicit confirmation.

## Phase B: Strategic Reporting

### 5. Profiling

```python
from scipy.stats import chi2_contingency

df_clean['Segment'] = final_labels
demo_df = to_dataframe('/workspace/data/casedata_gender.json',
                       '/workspace/data/casedata_age.json')
combined = pd.concat([df_clean, demo_df], axis=1)

for col in ['Gender', 'Age']:
    ct = pd.crosstab(combined['Segment'], combined[col])
    chi2, p, dof, _ = chi2_contingency(ct)
    print(f"{col}: chi2={chi2:.1f}, p={p:.4f} {'***' if p < 0.001 else ''}")
```

### 6. Persona Visualisation

Use `ask_user` to choose: interactive webapp or static charts.

Persona cards should include: emoji, name, tagline, segment size, key insight (highlighted), demographic split, top engagement metrics, representative quotes, strategic implication, and mean input variable scores with visual bars.

### 7. Write-back

> **NOT YET AVAILABLE** — requires write-back API (#541). For now, export as CSV.

**Critical:** ALL cases must get a segment code — excluded cases (DK, missing) get an "Unclassified" code:

```python
# Assign segments to ALL cases including excluded
all_segments = pd.Series('Unclassified', index=range(len(df)))
all_segments[df_clean.index] = [f'Segment_{l+1}' for l in final_labels]
all_segments.to_csv('/workspace/segments.csv', index=True)
```

**Overwrite protection** (when write-back is available): check if segment variable already exists. If so, present 3 options via `ask_user`: (1) Overwrite, (2) Save as new name, (3) Cancel and investigate existing.

### 8. TOC Update

> **NOT YET AVAILABLE** — requires TOC write API (#501). Save profiling tables to folder `Segmentation Analysis/<project name>`.

## Rules

1. Never cluster on demographic variables — use them for profiling only
2. Always standardise numeric inputs before clustering
3. Always handle "Don't Know" codes (99) — exclude from clustering, assign "Unclassified"
4. Always produce a validation report as a saved artifact before profiling
5. Do NOT proceed from Phase A to Phase B without explicit user confirmation
6. Recommend 3-6 segments — more than 6 is rarely actionable
7. Flag any segment smaller than 5% of total
8. Name segments with descriptive business labels, not "Cluster 1, 2, 3"
9. Weak silhouette scores (0.2-0.3) are normal for attitudinal data — note this in the report
10. Use `ask_user` at every checkpoint
