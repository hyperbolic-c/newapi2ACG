"""Independent JSON Schema structural gate; semantic checks run in Go tests.

JSON Schema formats are annotations here. Go validates timestamps, URLs, CIDRs,
reference instances, and source field coverage. No optional format plugins or
network fetches are used. Missing validation dependencies fail this gate.
"""
import json
from pathlib import Path

try:
    from jsonschema import Draft202012Validator
    import yaml
except ImportError:
    raise SystemExit("Schema gate unavailable: install test/schema/requirements.txt; no validation was performed.")

root = Path(__file__).resolve().parents[2]
for file in sorted((root / "schemas").rglob("*.json")):
    Draft202012Validator.check_schema(json.loads(file.read_text()))

count = 0
for fixture_dir, schema_dir, field in [
    ("resources", "acg/v1", "resource"),
    ("configs", "config", "config"),
]:
    cases = json.loads((root / "test/fixtures" / fixture_dir / "cases.json").read_text())
    for case in cases:
        schema = json.loads((root / "schemas" / schema_dir / (case["schema"] + ".json")).read_text())
        actual = Draft202012Validator(schema).is_valid(case[field])
        if actual != case["valid"]:
            raise SystemExit(f"Schema fixture failed: {case['name']}")
        count += 1

examples = 0
for category, kind in [("sources", "source"), ("instances", "instance"), ("jobs", "job"), ("targets", "target")]:
    schema = json.loads((root / "schemas/config" / (kind + ".json")).read_text())
    for file in sorted((root / "configs" / category).glob("*.yaml")):
        document = yaml.safe_load(file.read_text())
        if not Draft202012Validator(schema).is_valid(document):
            raise SystemExit(f"Schema validation failed: {file.relative_to(root)}")
        examples += 1
print(f"Schema documents, {count} shared fixtures and {examples} configuration examples: PASS (structural)")
