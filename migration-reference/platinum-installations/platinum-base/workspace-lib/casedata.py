"""
casedata.py — Helper for processing raw case data JSON files.

Raw case data contains per-respondent values for survey variables.
This module converts it to pandas DataFrames for custom analysis.

Supports two file formats:
  - Per-variable files (one variable per file, "variable" key)
  - Multi-variable files (multiple variables, "variables" key)

Usage:
    import sys; sys.path.insert(0, '/workspace/lib')
    from casedata import to_dataframe, cross_tabulate, filter_cases

    df = to_dataframe('/workspace/data/casedata_gender.json')
    df = to_dataframe('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json')
    xtab = cross_tabulate('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json')
"""

import json
from typing import Union

import pandas as pd


def load_casedata(source: Union[str, dict]) -> dict:
    """Load raw case data from a file path or dict.

    Args:
        source: Path to a JSON file, or an already-parsed dict.

    Returns:
        The case data dict (either per-variable or multi-variable format).
    """
    if isinstance(source, dict):
        return source
    with open(source) as f:
        return json.load(f)


def _normalize(data: dict) -> dict:
    """Normalize per-variable format to multi-variable format.

    If data has "variable" key (per-variable), convert to {"variables": {...}} format.
    If data already has "variables" key, return as-is.
    """
    if "variable" in data and "variables" not in data:
        varname = data["variable"]
        return {
            "variables": {
                varname: {
                    "description": data.get("description", ""),
                    "codes": data.get("codes", {}),
                    "data_type": data.get("data_type", "single"),
                    "values": data.get("values", []),
                }
            },
            "total_cases": data.get("total_cases", len(data.get("values", []))),
            "customer": data.get("customer", ""),
            "job": data.get("job", ""),
        }
    return data


def _load_and_merge(*sources) -> dict:
    """Load one or more sources and merge into multi-variable format."""
    merged = {"variables": {}, "total_cases": 0, "customer": "", "job": ""}
    for src in sources:
        data = _normalize(load_casedata(src))
        merged["variables"].update(data.get("variables", {}))
        merged["total_cases"] = data.get("total_cases", merged["total_cases"])
        merged["customer"] = data.get("customer", merged["customer"])
        merged["job"] = data.get("job", merged["job"])
    return merged


def _is_per_variable(data: dict) -> bool:
    """Check if data is in per-variable format."""
    return "variable" in data and "variables" not in data


def get_variable_info(source: Union[str, dict], varname: str = None) -> dict:
    """Get metadata for a single variable.

    Args:
        source: Path to JSON file or case data dict.
        varname: Variable name. Optional for per-variable format files.

    Returns:
        Dict with 'description', 'codes', 'data_type', and 'n_cases'.
    """
    data = load_casedata(source)
    if varname is None:
        if _is_per_variable(data):
            return {
                "description": data.get("description", ""),
                "codes": data.get("codes", {}),
                "data_type": data.get("data_type", "single"),
                "n_cases": len(data.get("values", [])),
            }
        raise ValueError("varname is required for multi-variable format files")
    data = _normalize(data)
    var = data["variables"][varname]
    return {
        "description": var.get("description", ""),
        "codes": var.get("codes", {}),
        "data_type": var.get("data_type", "single"),
        "n_cases": len(var.get("values", [])),
    }


def to_dataframe(
    *sources: Union[str, dict],
    labeled: bool = True,
) -> pd.DataFrame:
    """Convert one or more case data files to a pandas DataFrame.

    Rows = respondents, columns = variables.

    Args:
        *sources: One or more file paths, or already-parsed dicts.
            Each can be per-variable or multi-variable format.
        labeled: If True, map numeric codes to labels. If False, keep raw codes.

    Returns:
        DataFrame with one row per respondent and one column per variable.
    """
    data = _load_and_merge(*sources)
    variables = data.get("variables", {})

    columns = {}
    for varname, var in variables.items():
        values = var.get("values", [])
        codes = var.get("codes", {})
        data_type = var.get("data_type", "single")

        if labeled and data_type == "single" and codes:
            # Map numeric codes to labels
            col = []
            for v in values:
                if v is None:
                    col.append(None)
                else:
                    label = codes.get(str(v))
                    col.append(label if label is not None else v)
            columns[varname] = col
        elif labeled and data_type == "multi" and codes:
            # Map each code in multi-select lists to labels
            col = []
            for v in values:
                if v is None:
                    col.append(None)
                elif isinstance(v, list):
                    labels = [codes.get(str(c), str(c)) for c in v]
                    col.append(labels)
                else:
                    col.append(v)
            columns[varname] = col
        else:
            columns[varname] = values

    return pd.DataFrame(columns)


def cross_tabulate(
    source: Union[str, dict],
    row_var: str,
    col_var: str = None,
    weight_var: str = None,
    normalize: str = None,
) -> pd.DataFrame:
    """Cross-tabulate two variables from raw case data.

    Supports two calling patterns:
      - Per-variable files: cross_tabulate(row_file, col_file)
      - Multi-variable file: cross_tabulate(combined_file, 'RowVar', 'ColVar')

    Args:
        source: Path to row variable file (per-var) or combined file (multi-var).
        row_var: Path to col variable file (per-var) or row variable name (multi-var).
        col_var: Column variable name (multi-var only, ignored for per-var).
        weight_var: Weight variable name (multi-var) or weight file path (per-var).
        normalize: None (counts), 'col' (column %), 'row' (row %), 'all' (total %).

    Returns:
        Cross-tabulation DataFrame.
    """
    first = load_casedata(source)

    if _is_per_variable(first):
        # Per-variable format: row_var arg is the col file path
        merge_sources = [source, row_var]
        rv = first["variable"]
        cv = load_casedata(row_var)["variable"]
        weight_var_name = None
        if weight_var:
            merge_sources.append(weight_var)
            weight_var_name = load_casedata(weight_var)["variable"]
        data = _load_and_merge(*merge_sources)
        weight_var = weight_var_name
    else:
        # Multi-variable format: row_var and col_var are variable name strings
        data = _normalize(first)
        rv = row_var
        cv = col_var

    variables = data.get("variables", {})

    row_vals = variables[rv]["values"]
    col_vals = variables[cv]["values"]
    row_codes = variables[rv].get("codes", {})
    col_codes = variables[cv].get("codes", {})

    n = min(len(row_vals), len(col_vals))

    # Build Series with labels
    rows = []
    cols = []
    weights = []

    weight_vals = None
    if weight_var and weight_var in variables:
        weight_vals = variables[weight_var]["values"]

    for i in range(n):
        r_val, c_val = row_vals[i], col_vals[i]
        if r_val is None or c_val is None:
            continue
        # Handle multi-select in row or col by expanding
        r_items = r_val if isinstance(r_val, list) else [r_val]
        c_items = c_val if isinstance(c_val, list) else [c_val]
        for r in r_items:
            for c_item in c_items:
                r_label = row_codes.get(str(r), str(r))
                c_label = col_codes.get(str(c_item), str(c_item))
                rows.append(r_label)
                cols.append(c_label)
                if weight_vals and i < len(weight_vals) and weight_vals[i] is not None:
                    weights.append(weight_vals[i])
                else:
                    weights.append(1)

    row_series = pd.Series(rows, name=rv)
    col_series = pd.Series(cols, name=cv)

    if weight_var and weight_vals:
        weight_series = pd.Series(weights, name="weight")
        # Weighted cross-tab: group and sum weights
        df_temp = pd.DataFrame({rv: row_series, cv: col_series, "weight": weight_series})
        xtab = df_temp.pivot_table(index=rv, columns=cv, values="weight", aggfunc="sum", fill_value=0)
    else:
        xtab = pd.crosstab(row_series, col_series)

    if normalize == "col":
        xtab = xtab.div(xtab.sum(axis=0), axis=1) * 100
    elif normalize == "row":
        xtab = xtab.div(xtab.sum(axis=1), axis=0) * 100
    elif normalize == "all":
        xtab = xtab / xtab.values.sum() * 100

    return xtab


def filter_cases(*sources: Union[str, dict], **conditions) -> pd.DataFrame:
    """Filter respondents by variable values.

    Args:
        *sources: One or more file paths or case data dicts.
        **conditions: Variable=value pairs. Value can be a single code or list of codes.
            E.g., filter_cases(path1, path2, Gender=1, Age=[1, 2, 3])

    Returns:
        Filtered DataFrame (labeled, same format as to_dataframe).
    """
    data = _load_and_merge(*sources)
    # Build unlabeled df from merged data
    df = to_dataframe(data, labeled=False)
    mask = pd.Series(True, index=df.index)

    variables = data.get("variables", {})

    for varname, allowed in conditions.items():
        if varname not in df.columns:
            continue
        if not isinstance(allowed, list):
            allowed = [allowed]

        data_type = variables.get(varname, {}).get("data_type", "single")
        if data_type == "multi":
            # For multi-select, check if any allowed code is in the list
            mask &= df[varname].apply(
                lambda v: isinstance(v, list) and any(c in v for c in allowed)
                if v is not None else False
            )
        else:
            mask &= df[varname].isin(allowed)

    # Return labeled version of filtered data
    filtered = df[mask].copy()

    # Apply labels to the filtered result
    for varname in filtered.columns:
        var = variables.get(varname, {})
        codes = var.get("codes", {})
        dt = var.get("data_type", "single")
        if codes and dt == "single":
            filtered[varname] = filtered[varname].map(
                lambda v: codes.get(str(int(v)), v) if pd.notna(v) and v is not None else v
            )

    return filtered.reset_index(drop=True)
