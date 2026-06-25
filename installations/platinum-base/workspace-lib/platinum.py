"""
platinum.py — Helper for processing PlatinumData JSON files.

PlatinumData is the structured output from cross-tabulation queries.
This module converts it to pandas DataFrames for analysis.

Usage:
    import sys; sys.path.insert(0, '/workspace/lib')
    from platinum import to_dataframe, get_base_sizes, get_meta

    df = to_dataframe('/workspace/data/gender_x_age.json')          # col %
    df = to_dataframe('/workspace/data/gender_x_age.json', 'freq')  # counts
    bases = get_base_sizes('/workspace/data/gender_x_age.json')
    meta = get_meta('/workspace/data/gender_x_age.json')
"""

import json
from typing import Union

import pandas as pd


def load_table(source: Union[str, dict]) -> dict:
    """Load PlatinumData from a file path or dict.

    Args:
        source: Path to a JSON file, or an already-parsed dict.

    Returns:
        The PlatinumData dict.
    """
    if isinstance(source, dict):
        return source
    with open(source) as f:
        return json.load(f)


def to_dataframe(
    source: Union[str, dict],
    metric: str = "colpc",
    include_base: bool = False,
) -> pd.DataFrame:
    """Convert PlatinumData to a pandas DataFrame.

    Args:
        source: Path to JSON file or PlatinumData dict.
        metric: Which cell metric to extract. One of:
            'colpc' — column percentages (default, 0–100 scale)
            'rowpc' — row percentages (0–100 scale)
            'freq'  — raw frequency counts
        include_base: If True, include Base/Spacer rows. Default False.

    Returns:
        DataFrame with side labels as index, top labels as columns.
    """
    data = load_table(source)
    top_vecs = data.get("top", {}).get("vecs", [])
    side_vecs = data.get("side", {}).get("vecs", [])
    rows = data.get("cells", {}).get("rows", [])

    if metric not in ("colpc", "rowpc", "freq"):
        raise ValueError(f"metric must be 'colpc', 'rowpc', or 'freq', got '{metric}'")

    # Filter top columns: only Code/Net/Arith (skip Base/Spacer/Stat)
    skip_top_types = {"Base", "Spacer", "Empty"}
    top_mask = [v.get("type") not in skip_top_types for v in top_vecs]
    top_labels = [v.get("label", f"col_{i}") for i, v in enumerate(top_vecs) if top_mask[i]]

    # Build row data
    needs_scale = metric in ("colpc", "rowpc")
    result = []
    row_labels = []
    for i, side_vec in enumerate(side_vecs):
        vec_type = side_vec.get("type", "Code")
        if not include_base and vec_type in ("Base", "Spacer", "Empty"):
            continue
        row_labels.append(side_vec.get("label", f"row_{i}"))
        if i < len(rows):
            cells = rows[i].get("cell", [])
            row_data = []
            for j, use in enumerate(top_mask):
                if use:
                    cell = cells[j] if j < len(cells) else {}
                    raw = cell.get(metric, 0) or 0
                    row_data.append(raw * 100 if needs_scale else raw)
            result.append(row_data)
        else:
            result.append([0] * len(top_labels))

    return pd.DataFrame(result, index=row_labels, columns=top_labels)


def get_base_sizes(source: Union[str, dict]) -> pd.Series:
    """Extract base (unweighted count) row as a Series.

    Finds the first Base-type row in the side axis and returns
    its freq values as a Series indexed by top column labels.

    Args:
        source: Path to JSON file or PlatinumData dict.

    Returns:
        Series with top labels as index, base counts as values.
    """
    data = load_table(source)
    top_vecs = data.get("top", {}).get("vecs", [])
    side_vecs = data.get("side", {}).get("vecs", [])
    rows = data.get("cells", {}).get("rows", [])

    skip_top_types = {"Base", "Spacer", "Empty"}
    top_mask = [v.get("type") not in skip_top_types for v in top_vecs]
    top_labels = [v.get("label", f"col_{i}") for i, v in enumerate(top_vecs) if top_mask[i]]

    # Find first Base row
    for i, side_vec in enumerate(side_vecs):
        if side_vec.get("type") == "Base" and i < len(rows):
            cells = rows[i].get("cell", [])
            values = []
            for j, use in enumerate(top_mask):
                if use:
                    cell = cells[j] if j < len(cells) else {}
                    values.append(cell.get("freq", 0) or 0)
            return pd.Series(values, index=top_labels, name="base")

    return pd.Series(dtype=float, name="base")


def get_meta(source: Union[str, dict]) -> dict:
    """Extract table metadata.

    Args:
        source: Path to JSON file or PlatinumData dict.

    Returns:
        Dict with keys: top, side, filter, weight, sigLevel, name, schemaVersion.
    """
    data = load_table(source)
    meta = data.get("meta", {})
    return {
        "top": meta.get("top", ""),
        "side": meta.get("side", ""),
        "filter": meta.get("filter", ""),
        "weight": meta.get("weight", ""),
        "sigLevel": meta.get("sigLevel"),
        "name": data.get("name", ""),
        "schemaVersion": data.get("schemaVersion", ""),
    }
