#!/usr/bin/env python3
"""Extract Go membership only from bundled models.md; models.dev enriches only.
Python 3.11+. Schema 1 stays compatible with existing plugin consumers.
"""
import argparse
import datetime
import hashlib
import json
import re
import sys
import tomllib
from decimal import Decimal
from pathlib import Path

def fail(message):
    raise ValueError(message)

def cells(line):
    return [v.strip().replace(r"\|", "|") for v in
            re.split(r"(?<!\\)\|", line.strip().strip("|"))]

def parse_context(value):
    if value in ("", "—", "-", "N/A"):
        return 0
    m = re.fullmatch(r"(\d+(?:\.\d+)?)\s*([KM]?)", value.replace(",", ""))
    if not m:
        fail(f"unknown Context: {value!r}")
    return int(Decimal(m[1]) * {"": 1, "K": 1000, "M": 1000000}[m[2]])

def parse_models_md(text):
    catalog, seen, header = [], set(), None
    for number, line in enumerate(text.splitlines(), 1):
        if not line.lstrip().startswith("|"):
            header = None
            continue
        row = cells(line)
        if any(x == "Id" or x.startswith("Id (") for x in row):
            header = ["Id" if x.startswith("Id (") else x for x in row]
            if not {"Id", "Name", "Context", "Efforts", "Min plan"}.issubset(header):
                fail(f"line {number}: missing required columns")
            if len(set(header)) != len(header):
                fail("duplicate columns")
            continue
        if all(re.fullmatch(r":?-+:?", c) for c in row):
            continue
        if header is None:
            if any("Min plan" in c for c in row) or row[0].startswith("`"):
                fail(f"line {number}: model table format changed")
            continue
        if len(row) != len(header):
            fail(f"line {number}: wrong cell count")
        d = dict(zip(header, row))
        mid = d["Id"].strip("`")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/-]*", mid):
            fail(f"invalid ID: {mid}")
        if mid.casefold() in seen:
            fail(f"duplicate ID: {mid}")
        seen.add(mid.casefold())
        plan = re.sub(r" and above$", "", d["Min plan"])
        if plan not in {"Go", "GOAT", "Pro", "Max"}:
            fail(f"{mid}: unknown plan {d['Min plan']!r}")
        efforts = [] if d["Efforts"] in ("", "—", "-") else [
            x.strip() for x in d["Efforts"].split(",")]
        if any(not re.fullmatch(r"[a-z][a-z0-9_-]*", x) for x in efforts):
            fail(f"{mid}: invalid efforts")
        if not d["Name"]:
            fail(f"{mid}: empty name")
        catalog.append({
            "id": mid, "display": d["Name"].removesuffix(" (latest)"),
            "description": d.get("Best for", ""), "min_plan": plan,
            "context": parse_context(d["Context"]), "context_raw": d["Context"],
            "reasoning_efforts": efforts,
            "pricing_raw": next((v for k, v in d.items() if k.startswith("$/")), ""),
        })
    if len(catalog) < 10:
        fail(f"catalog unexpectedly small: {len(catalog)}")
    return catalog

def load_models_dev(root):
    entries = []
    for path in sorted(Path(root).rglob("*.toml")):
        data = tomllib.loads(path.read_text(encoding="utf-8"))
        rel = path.relative_to(root)
        for field in ("context", "output"):
            v = data.get("limit", {}).get(field, 0)
            if not isinstance(v, int) or isinstance(v, bool) or v < 0:
                fail(f"{rel}: invalid {field}")
        entries.append((rel.with_suffix("").as_posix(), rel.as_posix(), data))
    if not entries:
        fail("no models.dev TOML files")
    return entries

def match_models_dev(entries, mid):
    exact = [e for e in entries if e[0].casefold() == mid.casefold()]
    if len(exact) == 1:
        return exact[0]
    # Exact case-insensitive basename only; preserve punctuation and variants.
    short = mid.rsplit("/", 1)[-1].casefold()
    matches = [e for e in entries if e[0].rsplit("/", 1)[-1].casefold() == short]
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        print(f"warning: ambiguous metadata for {mid}", file=sys.stderr)
    return None

def build_models(catalog, entries, previous):
    previous = {m["id"].casefold(): m for m in previous}
    result, shorts = [], set()
    for row in catalog:
        if row["min_plan"] != "Go":
            continue
        m = dict(row)
        mid = m["id"]
        short = mid.rsplit("/", 1)[-1].split(":")[0].casefold()
        if short in shorts:
            fail(f"Go short-name collision: {mid}")
        shorts.add(short)
        match = match_models_dev(entries, mid)
        meta = match[2] if match else {}
        limits = meta.get("limit", {})
        ref = match[1] if match else ""
        # The deployment's documented window outranks another provider's
        # integer limit. Decimal K/M is explicit, not guessed as binary.
        context = row["context"] or limits.get("context", 0) or 200000
        cs = "models.md" if row["context"] else (
            "modelsdev" if limits.get("context") else "default")
        output, osource = limits.get("output", 0), "modelsdev"
        if not output or output > context:
            old = previous.get(mid.casefold(), {})
            output = old.get("output", 0)
            if (old.get("output_source") in ("modelsdev", "carry")
                    and old.get("output_ref") and 0 < output <= context):
                osource, ref = "carry", old.get("output_ref", "")
            else:
                output = min(context, 32768 if context <= 204800 else 65536)
                osource = "fallback"
        modalities = meta.get("modalities", {})
        m.update({
            "context": context, "context_source": cs,
            "output": output, "output_source": osource,
            "output_ref": ref if osource in ("modelsdev", "carry") else "",
            "min_plan_source": "models.md", "reasoning_efforts_source": "models.md",
            "vision": "image" in modalities["input"] if "input" in modalities else None,
            "vision_source": "modelsdev" if "input" in modalities else "unresolved",
            "metadata_ref": match[1] if match else "",
        })
        result.append(m)
    if not 10 <= len(result) <= 300:
        fail(f"Go count outside bounds: {len(result)}")
    return sorted(result, key=lambda m: m["id"].casefold())

def validate_drift(models, previous, allow=False):
    old = {m["id"].casefold() for m in previous}
    new = {m["id"].casefold() for m in models}
    if old:
        added, removed = new - old, old - new
        print(f"roster diff: +{len(added)} -{len(removed)}", file=sys.stderr)
        if max(len(added), len(removed)) > max(10, len(old) * .25) and not allow:
            fail("large roster drift; review and use --allow-large-drift")

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    for arg in ("models-md", "package", "models-dev", "models-dev-rev", "out", "gen"):
        ap.add_argument("--" + arg, required=True)
    ap.add_argument("--prev", default="")
    ap.add_argument("--package-integrity", default="")
    ap.add_argument("--allow-large-drift", action="store_true")
    a = ap.parse_args()
    source = Path(a.models_md).read_bytes()
    package = json.loads(Path(a.package).read_text())
    if package.get("name") != "command-code" or not package.get("version"):
        fail("not a versioned command-code package")
    prev = json.loads(Path(a.prev).read_text()) if a.prev and Path(a.prev).exists() else {}
    catalog = parse_models_md(source.decode("utf-8"))
    models = build_models(catalog, load_models_dev(a.models_dev), prev.get("models", []))
    validate_drift(models, prev.get("models", []), a.allow_large_drift)
    doc = {
        "schema_version": 1, "source": "command-code bundled models.md",
        "source_cli_version": package["version"],
        "source_path": "dist/bundled/command-code-knowledge/reference/models.md",
        "source_sha256": hashlib.sha256(source).hexdigest(),
        "source_package_integrity": a.package_integrity,
        "models_dev_rev": a.models_dev_rev,
        "counts": {"catalog": len(catalog), "entitled": len(models)}, "models": models,
    }
    compare = {k: v for k, v in prev.items() if k != "fetched_at"}
    doc["fetched_at"] = prev["fetched_at"] if doc == compare else (
        datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
    lines = [
        f"// Code generated by scripts/extract-models.py from command-code {package['version']} models.md. DO NOT EDIT.",
        "package main", "", "var modelTable = []modelDef{",
    ]
    for m in models:
        lines.append("\t{%s, %s, %d, %d}," % (
            json.dumps(m["id"], ensure_ascii=False), json.dumps(m["display"], ensure_ascii=False),
            m["context"], m["output"]))
    lines.extend(["}", ""])
    # Validate both outputs before replacing either file.
    Path(a.out).write_text(json.dumps(doc, indent=2) + "\n")
    Path(a.gen).write_text("\n".join(lines))
    print(f"command-code {package['version']}: catalog={len(catalog)} Go={len(models)}")

if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError) as exc:
        print(f"extract-models: error: {exc}", file=sys.stderr)
        sys.exit(2)
