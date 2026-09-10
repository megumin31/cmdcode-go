#!/usr/bin/env python3
"""Extract the CommandCode Go-plan model roster from an installed CLI bundle.

The official CLI ships a static model catalog in dist/cli.mjs (the XR map:
id/label/description/contextWindow/...) plus a category map (jr:
opensource vs premium) and a plan-gating map (Br: allowedCategories +
blockedModels per plan). There is no server endpoint for the list, so this
script mirrors the CLI's own evaluateModelAccess() for individual-go:

  entitled = catalog ids
             minus premium-category ids
             minus provider-qualified blockedModels
             (uncategorized ids fail OPEN, exactly like the CLI:
              getModelCategory() == null -> allowed)

Outputs:
  --out models.json             data contract consumed by the plugin at
                                runtime (models_url) and by --gen.
  --gen go/models_generated.go  compiled fallback table (var modelTable).

Output budgets: the bundle carries maxOutputTokens for only a handful of
entries, so budgets resolve as: bundle value > models.dev [limit].output
(--models-dev, anomalyco/models.dev canonical data) > carried value from
--prev models.json (same id, case-insensitive) > 32768 when context <=
204800 > 65536. Contexts resolve as Tr override > XR contextWindow >
models.dev limit.context (only when the CLI itself falls back to its
200000 default) > 200000. Every entry records output_source/context_source
so each number is auditable. Display names drop the trailing " (latest)"
suffix (release noise).

Anchors are literal markers (inputModalities, allowedCategories,
blockedModels, "opensource"/"premium"); minified variable names are
resolved dynamically, never hardcoded. Context windows resolve as
Tr override > XR contextWindow > CLI default (Er=200000), mirroring
getContextLimit(). Stdlib only.
"""

import argparse
import datetime
import json
import re
import sys
from pathlib import Path

SCHEMA_VERSION = 1
MUST_CONTAIN = ["deepseek/deepseek-v4-flash"]
MIN_ENTITLED = 10
MAX_ENTITLED = 300
SMALL_CONTEXT_MAX = 204800
SMALL_CONTEXT_OUTPUT = 32768
DEFAULT_OUTPUT = 65536
DEFAULT_CONTEXT = 200000  # CLI's Er fallback in getContextLimit()

def fail(msg):
    print(f"extract-models: error: {msg}", file=sys.stderr)
    sys.exit(2)


def warn(msg):
    print(f"extract-models: warn: {msg}", file=sys.stderr)


def find_category_vars(bundle):
    """Resolve the minified vars holding "opensource"/"premium"."""
    m_oss = re.search(r'([$\w]+)="opensource"', bundle)
    m_pre = re.search(r'([$\w]+)="premium"', bundle)
    if not m_oss or not m_pre:
        fail("category vars (opensource/premium) not found")
    return m_oss.group(1), m_pre.group(1)


def parse_plan_block(bundle):
    """Parse Br's individual-go entry: allowed categories + blocked models."""
    m = re.search(
        r'\{"individual-go":\{allowedCategories:\[([^\]]*)\]'
        r'(?:,blockedModels:\[([^\]]*)\])?',
        bundle,
    )
    if not m:
        fail("individual-go plan block not found")
    allowed = [v.strip() for v in m.group(1).split(",") if v.strip()]
    blocked = re.findall(r'"([^"]+)"', m.group(2) or "")
    return allowed, blocked


def parse_categories(bundle):
    """Map catalog id -> "opensource" | "premium". Absent id = uncategorized.

    Shapes: "id":Ur() | "id":Fr(ProviderVar) | "id":{provider:X,category:Y}.
    """
    cats = {}
    providers = {}  # id -> provider var (or "cai" for Ur())
    for m in re.finditer(r'"([^"]+)":Ur\(\)', bundle):
        cats[m.group(1)] = "opensource"
        providers[m.group(1)] = "cai"
    for m in re.finditer(r'"([^"]+)":Fr\((\w+)\)', bundle):
        cats[m.group(1)] = "premium"
        providers[m.group(1)] = m.group(2)
    for m in re.finditer(
        r'"([^"]+)":\{provider:(\w+),category:([$\w]+)\}', bundle
    ):
        cats[m.group(1)] = m.group(3)  # resolved to a name below
        providers[m.group(1)] = m.group(2)
    return cats, providers


def resolve_provider_names(bundle, oss_var, pre_var):
    """Map minified provider vars (Or/Lr/Dr/Nr/...) to registry names."""
    names = {}
    for m in re.finditer(r'(\w+)="([a-z][a-z0-9-]*)"', bundle):
        var, name = m.group(1), m.group(2)
        if name in (
            "anthropic",
            "openai",
            "vercel-ai-gateway",
            "openrouter",
            "cai",
        ):
            names[var] = name
    names["cai"] = "cai"
    return names


def brace_end(text, start):
    """Index just past the brace block opened at text[start] == '{'.

    Strings are honored so braces inside labels cannot unbalance the scan.
    """
    depth = 0
    i = start
    in_str = False
    esc = False
    quote = ""
    while i < len(text):
        ch = text[i]
        if in_str:
            if esc:
                esc = False
            elif ch == "\\":
                esc = True
            elif ch == quote:
                in_str = False
        else:
            if ch in ('"', "'"):
                in_str, quote = True, ch
            elif ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
                if depth == 0:
                    return i + 1
        i += 1
    fail("unterminated catalog entry")


def parse_entry(text):
    """Pull the fields the plugin needs out of one {id:..., ...} block."""
    entry = {}
    m = re.search(r'id:"([^"]+)"', text)
    if not m:
        return None
    entry["id"] = m.group(1)
    m = re.search(r'inputModalities:\[([^\]]*)\]', text)
    entry["vision"] = bool(m and '"image"' in m.group(1))
    for key in ("label", "name", "description"):
        m = re.search(key + r':"((?:[^"\\]|\\.)*)"', text)
        if m:
            entry[key] = m.group(1)
    m = re.search(r'contextWindow:(\d+(?:\.\d+)?e\d+|\d+)', text)
    try:
        entry["context"] = int(float(m.group(1))) if m else 0
    except ValueError:
        entry["context"] = 0
    m = re.search(r'maxOutputTokens:(\d+)', text)
    entry["max_output"] = int(m.group(1)) if m else 0
    m = re.search(r'reasoningEfforts:\[([^\]]*)\]', text)
    entry["efforts"] = re.findall(r'"([^"]+)"', m.group(1)) if m else []
    m = re.search(r'badge:"([^"]+)"', text)
    entry["badge"] = m.group(1) if m else None
    entry["hidden"] = "hidden:!0" in text.replace(" ", "")
    return entry


def parse_catalog(bundle):
    catalog = {}
    dupes = set()
    for m in re.finditer(r'\{id:"([^"]+)",inputModalities:', bundle):
        entry = parse_entry(bundle[m.start() : brace_end(bundle, m.start())])
        if entry is None:
            continue
        if entry["id"] in catalog:
            dupes.add(entry["id"])
        catalog[entry["id"]] = entry
    if dupes:
        warn(f"duplicate catalog entries (last wins): {sorted(dupes)}")
    return catalog

def parse_context_table(bundle):
    """Tr override map: id -> context window, mirroring getContextLimit().

    Resolution order per the CLI is Tr entry > XR contextWindow > Er.
    """
    m = re.search(r"Tr=new Map\(\[(.*?)\]\)", bundle)
    if not m:
        warn("Tr context table not found, XR values only")
        return {}
    try:
        return {a: int(float(b)) for a, b in
                re.findall(r'\["([^"]+)",(\d+(?:\.\d+)?e\d+|\d+)\]', m.group(1))}
    except ValueError:
        warn("Tr context table partially unparseable")
        return {}

def load_models_dev(root):
    """Index anomalyco/models.dev canonical limits by normalized stem.

    Returns {norm_stem: [(relpath, output, context)]}. Entries without a
    [limit] table are skipped: models.dev also leaves blanks, and those
    fall through to the carry/heuristic chain.
    """
    index = {}
    try:
        import tomllib
    except ImportError:
        warn("tomllib unavailable, models.dev disabled")
        return index
    files = sorted(Path(root).rglob("*.toml"))
    if not files:
        warn(f"no models.dev TOML under {root}")
    for path in files:
        try:
            doc = tomllib.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError) as e:
            warn(f"models.dev unreadable {path}: {e}")
            continue
        lim = doc.get("limit") or {}
        try:
            output = int(lim["output"]) if lim.get("output") else 0
            context = int(lim["context"]) if lim.get("context") else 0
        except (ValueError, TypeError):
            warn(f"models.dev bad limit {path}")
            continue
        if not output and not context:
            continue
        rel = str(path.relative_to(root))
        index.setdefault(norm_id(path.stem), []).append((rel, output, context))
    return index


def norm_id(s):
    return re.sub(r"[-_.:/ ]+", "", s.lower())


def match_models_dev(index, gateway_id):
    """Match a gateway id to one models.dev entry.

    Exact normalized stem first; a single trailing -free fallback covers
    free-tier aliases of the same deployment (ling-3.0-flash-fin style
    near-misses stay unmatched by design). Ambiguous matches are
    dropped loudly.
    """
    short = gateway_id.split("/")[-1].split(":")[0]
    cands = index.get(norm_id(short), [])
    if len(cands) == 1:
        return cands[0] + ("exact",)
    if not cands and short.lower().endswith("-free"):
        base = short[: -len("-free")]
        cands = index.get(norm_id(base), [])
        if len(cands) == 1:
            return cands[0] + ("free-variant",)
    if len(cands) > 1:
        warn(f"{gateway_id}: ambiguous models.dev matches, skipped: "
             f"{[c[0] for c in cands]}")
    return None


def display_name(entry):
    label = entry.get("label") or entry.get("name") or entry["id"]
    if label.endswith(" (latest)"):
        label = label[: -len(" (latest)")]
    return label


def go_quote(s):
    return (
        '"'
        + s.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")
        + '"'
    )


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--cli", required=True, help="path to dist/cli.mjs")
    ap.add_argument("--package", default="", help="path to package.json")
    ap.add_argument("--cli-version", default="", help="override version")
    ap.add_argument("--prev", default="", help="previous models.json")
    ap.add_argument("--models-dev", default="",
                    help="anomalyco/models.dev models/ dir for limits "
                         "the CLI bundle leaves unspecified")
    ap.add_argument("--models-dev-rev", default="",
                    help="models.dev revision recorded for provenance")
    ap.add_argument("--out", required=True, help="models.json output path")
    ap.add_argument("--gen", required=True, help="models_generated.go path")
    args = ap.parse_args()

    bundle = Path(args.cli).read_text(encoding="utf-8", errors="replace")
    version = args.cli_version
    if not version and args.package:
        try:
            version = json.loads(Path(args.package).read_text())["version"]
        except (OSError, ValueError, KeyError) as e:
            warn(f"package.json unreadable: {e}")
    if not version:
        version = "unknown"
        warn("CLI version unknown (no --package/--cli-version)")

    prev_budgets = {}
    prev_doc = None
    if args.prev and Path(args.prev).exists():
        try:
            prev_doc = json.loads(Path(args.prev).read_text())
            for m in prev_doc.get("models", []):
                if m.get("id") and m.get("output"):
                    prev_budgets[m["id"].lower()] = int(m["output"])
        except (OSError, ValueError) as e:
            prev_doc = None
            warn(f"previous models.json unreadable, budgets reset: {e}")

    oss_var, pre_var = find_category_vars(bundle)
    allowed, blocked = parse_plan_block(bundle)
    if oss_var not in allowed:
        fail(f"opensource var {oss_var} not in Go allowedCategories")
    cats, providers = parse_categories(bundle)
    prov_names = resolve_provider_names(bundle, oss_var, pre_var)

    # Resolve {provider:X,category:Y} shapes to category names.
    for mid, cat in list(cats.items()):
        if cat not in ("opensource", "premium"):
            cats[mid] = "opensource" if cat == oss_var else (
                "premium" if cat == pre_var else None
            )
            if cats[mid] is None:
                warn(f"{mid}: unresolvable category var {cat}, fail-open")
                del cats[mid]

    def blocked_match(mid):
        var = providers.get(mid, "cai")
        full = f"{prov_names.get(var, var)}:{mid}".lower()
        if full in {b.lower() for b in blocked}:
            return True
        if var not in prov_names:
            # Provider name unresolvable: match on the model part alone so
            # a blocked model can never slip in silently.
            warn(f"{mid}: provider var {var} unresolvable, model-part match")
            return mid.lower() in {b.split(':')[-1].lower() for b in blocked}
        return False

    catalog = parse_catalog(bundle)
    if not catalog:
        fail("no catalog entries parsed")
    ctx_table = parse_context_table(bundle)
    md_index = load_models_dev(args.models_dev) if args.models_dev else {}
    md_rev = args.models_dev_rev
    def resolve_context(mid, entry):
        if mid in ctx_table:
            return ctx_table[mid], False
        if entry and entry["context"]:
            return entry["context"], False
        # Case drift between maps (MiniMaxAI/ vs minimax/) is real;
        # retry case-insensitively before falling back to Er.
        lowered = mid.lower()
        for key, val in ctx_table.items():
            if key.lower() == lowered:
                return val, False
        return DEFAULT_CONTEXT, True
    counts = {"catalog": len(catalog)}
    for key in ("opensource", "premium", "uncategorized"):
        counts[key] = 0
    counts["blocked"] = 0
    counts["synthesized"] = 0
    counts["modelsdev"] = 0
    # Categorized but absent from the XR catalog (no label/context of its
    # own): synthesize from the Tr table so an entitled model is never
    # dropped for lack of display metadata.
    synthesized = set()
    for mid in sorted(set(cats) - set(catalog)):
        cat = cats[mid]
        if cat == "premium" or blocked_match(mid):
            continue
        short = mid.split("/")[-1].split(":")[0]
        catalog[mid] = {
            "id": mid, "vision": False, "label": short,
            "description": "", "context": 0, "max_output": 0,
            "efforts": [], "badge": None, "hidden": False,
        }
        synthesized.add(mid)
        warn(f"{mid}: no catalog metadata, synthesized entry")

    # A synthesized id shadowing a catalogued short name is unroutable by
    # short name and usually a stale remnant: drop it loudly instead.
    shorts = {}
    for mid in catalog:
        shorts.setdefault(mid.split("/")[-1].split(":")[0].lower(), []).append(mid)
    dropped = 0
    for mid in sorted(synthesized):
        short = mid.split("/")[-1].split(":")[0].lower()
        if len(shorts[short]) > 1:
            del catalog[mid]
            synthesized.discard(mid)
            dropped += 1
            warn(f"{mid}: short-name collision, dropped")
    counts["synthesized"] = len(synthesized)
    counts["dropped"] = dropped

    models = []
    for mid in catalog:
        cat = cats.get(mid)
        counts["opensource" if cat == "opensource" else
               "premium" if cat == "premium" else "uncategorized"] += 1
        if cat == "premium":
            continue
        if blocked_match(mid):
            counts["blocked"] += 1
            continue
        e = catalog[mid]
        context, ctx_default = resolve_context(mid, e)
        context_source = "cli"
        md = match_models_dev(md_index, mid) if md_index else None
        md_output, md_context, md_ref, md_kind = ((md[1], md[2], md[0], md[3])
                                                 if md else (0, 0, "", ""))
        if ctx_default and md_context:
            context, context_source = md_context, "modelsdev"
        elif ctx_default:
            context_source = "default"
        if e["max_output"]:
            output, output_source = e["max_output"], "bundle"
        # Coherence guard with unit tolerance: sources mix decimal and
        # binary K/M (262144 vs 256000 is the same 256K), so allow 5%.
        elif md_output and md_output <= int(context * 1.05):
            output, output_source = md_output, "modelsdev"
        elif md_output:
            warn(f"{mid}: models.dev output {md_output} exceeds context "
                 f"{context}, ignored ({md_ref})")
            output, output_source = None, ""
        else:
            output, output_source = None, ""
        if not output_source:
            if mid.lower() in prev_budgets:
                output, output_source = prev_budgets[mid.lower()], "carry"
            elif context <= SMALL_CONTEXT_MAX:
                output, output_source = SMALL_CONTEXT_OUTPUT, "heuristic"
            else:
                output, output_source = DEFAULT_OUTPUT, "heuristic"
        if output_source == "modelsdev" or context_source == "modelsdev":
            counts["modelsdev"] += 1
        models.append({
            "id": mid,
            "display": display_name(e),
            "description": e.get("description", ""),
            "context": context,
            "context_source": context_source,
            "output": output,
            "output_source": output_source,
            "output_ref": md_ref if output_source == "modelsdev" else "",
            "category": cat,
            "vision": e["vision"],
            "reasoning_efforts": e["efforts"],
            "badge": e["badge"],
            "hidden": e["hidden"],
        })
    models.sort(key=lambda m: m["id"].lower())
    counts["entitled"] = len(models)

    missing = [i for i in MUST_CONTAIN
               if i.lower() not in {m["id"].lower() for m in models}]
    if missing:
        warn(f"must-contain ids absent from entitled set: {missing}")
    if not MIN_ENTITLED <= len(models) <= MAX_ENTITLED:
        fail(f"entitled count {len(models)} outside "
             f"[{MIN_ENTITLED},{MAX_ENTITLED}]")

    fetched_at = datetime.datetime.now(datetime.timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ")
    rev = md_rev
    if (prev_doc and prev_doc.get("schema_version") == SCHEMA_VERSION
            and prev_doc.get("source_cli_version") == version
            and prev_doc.get("models") == models):
        # Byte-identical output: the scheduled Action becomes a no-op
        # instead of churning fetched_at every day.
        fetched_at = prev_doc.get("fetched_at", fetched_at)
        rev = prev_doc.get("models_dev_rev", rev)
        print("extract-models: roster unchanged, keeping " + fetched_at,
              file=sys.stderr)
    doc = {
        "schema_version": SCHEMA_VERSION,
        "source": "command-code CLI bundle (XR catalog + plan gating)",
        "source_cli_version": version,
        "models_dev_rev": rev,
        "fetched_at": fetched_at,
        "counts": counts,
        "blocked_models": sorted(blocked),
        "models": models,
    }
    Path(args.out).write_text(json.dumps(doc, indent=2) + "\n")
    lines = [
        "// Code generated by scripts/extract-models.py from command-code "
        f"{version}. DO NOT EDIT.",
        "//",
        "//\tRegenerate: python3 scripts/extract-models.py --cli <npm-root>/command-code/dist/cli.mjs \\",
        "//\t  --package <npm-root>/command-code/package.json --prev models.json --models-dev <models.dev>/models --out models.json --gen go/models_generated.go",
        "package main",
        "",
        "var modelTable = []modelDef{",
    ]
    for m in models:
        lines.append(
            f"\t{{{go_quote(m['id'])}, {go_quote(m['display'])}, "
            f"{m['context']}, {m['output']}}},"
        )
    lines.append("}")
    lines.append("")
    Path(args.gen).write_text("\n".join(lines))

    print(f"extract-models: cli={version} entitled={len(models)} "
          f"catalog={counts['catalog']} premium={counts['premium']} "
          f"uncategorized={counts['uncategorized']} "
          f"blocked={counts['blocked']} modelsdev={counts['modelsdev']}",
          file=sys.stderr)


if __name__ == "__main__":
    main()
