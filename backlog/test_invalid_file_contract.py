"""Parity contract tests for invalid-file show/claim diagnostics.

Contract: docs/diagnostics/invalid-file-warning-contract.md
Fixtures: testdata/invalid-file-diagnostics/
"""

from __future__ import annotations

import json
import re
import shutil
from pathlib import Path

import pytest
from click.testing import CliRunner

from backlog.cli import cli

REPO_ROOT = Path(__file__).resolve().parents[1]
FIXTURE_ROOT = REPO_ROOT / "testdata" / "invalid-file-diagnostics"
MESSAGES_PATH = FIXTURE_ROOT / "expected" / "messages.json"
CONTRACT_DOC = REPO_ROOT / "docs" / "diagnostics" / "invalid-file-warning-contract.md"

ANSI_RE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


@pytest.fixture
def runner():
    return CliRunner()


@pytest.fixture(scope="module")
def contract() -> dict:
    assert MESSAGES_PATH.is_file(), f"missing {MESSAGES_PATH}"
    data = json.loads(MESSAGES_PATH.read_text())
    assert data.get("version", 0) >= 1
    assert data.get("conditions"), "conditions required"
    return data


def _normalize(text: str) -> str:
    text = ANSI_RE.sub("", text or "")
    return " ".join(text.split())


def _copy_case(tmp_path: Path, relative: str) -> Path:
    src = FIXTURE_ROOT / relative
    assert src.is_dir(), f"missing fixture {src}"
    dest = tmp_path / "case"
    shutil.copytree(src, dest)
    return dest


def test_contract_doc_enumerates_all_codes(contract):
    assert CONTRACT_DOC.is_file(), f"missing contract doc {CONTRACT_DOC}"
    doc = CONTRACT_DOC.read_text()
    codes = {c["code"] for c in contract["conditions"]}
    expected = {
        "missing_indexed_file",
        "malformed_frontmatter",
        "id_path_mismatch",
        "file_absent_from_index",
    }
    assert codes == expected
    for code in expected:
        assert code in doc
    # Claimability section must document mutation rules.
    assert "May mutate?" in doc or "may mutate" in doc.lower()
    assert "Cannot claim" in doc


def test_fixtures_match_condition_definitions(contract):
    for cond in contract["conditions"]:
        case_dir = FIXTURE_ROOT / cond["fixture"]
        assert case_dir.is_dir(), cond["code"]
        assert (case_dir / ".tasks" / "index.yaml").is_file()
        assert cond["warning_substrings"]
        assert cond["recovery"]

        if cond["code"] == "missing_indexed_file":
            assert cond["claim"]["may_mutate"] is False
            missing = (
                case_dir
                / ".tasks"
                / "01-phase"
                / "01-ms"
                / "01-epic"
                / "T001-missing.todo"
            )
            assert not missing.exists()
        elif cond["code"] == "malformed_frontmatter":
            assert cond["claim"]["may_mutate"] is True
            path = (
                case_dir
                / ".tasks"
                / "01-phase"
                / "01-ms"
                / "01-epic"
                / "T001-malformed.todo"
            )
            raw = path.read_text().lstrip()
            assert not raw.startswith("---")
        elif cond["code"] == "id_path_mismatch":
            assert cond["claim"]["may_mutate"] is True
            path = (
                case_dir
                / ".tasks"
                / "01-phase"
                / "01-ms"
                / "01-epic"
                / "T001-id-mismatch.todo"
            )
            assert "id: P9.M9.E9.T999" in path.read_text()
        elif cond["code"] == "file_absent_from_index":
            assert cond["claim"]["may_mutate"] is False
            index = (
                case_dir / ".tasks" / "01-phase" / "01-ms" / "01-epic" / "index.yaml"
            ).read_text()
            assert "T001-orphan.todo" not in index
            assert (
                case_dir
                / ".tasks"
                / "01-phase"
                / "01-ms"
                / "01-epic"
                / "T001-orphan.todo"
            ).is_file()

    healthy = (
        FIXTURE_ROOT
        / contract["healthy_control"]["fixture"]
        / ".tasks"
        / "01-phase"
        / "01-ms"
        / "01-epic"
        / "T001-healthy.todo"
    )
    assert healthy.is_file()


@pytest.mark.parametrize(
    "code",
    [
        "missing_indexed_file",
        "malformed_frontmatter",
        "id_path_mismatch",
        "file_absent_from_index",
    ],
)
def test_cli_assertions_when_enabled(runner, contract, tmp_path, monkeypatch, code):
    cond = next(c for c in contract["conditions"] if c["code"] == code)
    enabled = cond.get("implementation", {}).get("cli_assertions", {}).get(
        "python", False
    )
    if not enabled:
        pytest.skip(f"CLI assertions deferred to T002 for {code}")

    root = _copy_case(tmp_path, cond["fixture"])
    monkeypatch.chdir(root)

    show = runner.invoke(cli, ["show", cond["task_id"]])
    show_text = _normalize(show.output)
    if cond["show"].get("must_warn"):
        for sub in cond["warning_substrings"]:
            assert _normalize(sub) in show_text, show.output
    if cond["show"].get("must_show_body"):
        for sub in cond["show"].get("body_substrings", []):
            assert _normalize(sub) in show_text, show.output

    # Fresh tree for claim
    claim_root = _copy_case(tmp_path / "claim", cond["fixture"])
    monkeypatch.chdir(claim_root)
    claim = runner.invoke(
        cli, ["claim", cond["task_id"], "--no-content", "--agent=contract-test"]
    )
    claim_text = _normalize(claim.output)

    if cond["claim"]["may_mutate"]:
        assert claim.exit_code == 0, claim.output
        if cond["claim"].get("must_warn_before_mutate"):
            for sub in cond["warning_substrings"]:
                assert _normalize(sub) in claim_text, claim.output
    else:
        assert claim.exit_code != 0, claim.output
        for sub in cond["claim"].get("error_substrings", []):
            assert _normalize(sub) in claim_text, claim.output
        for sub in cond["warning_substrings"]:
            assert _normalize(sub) in claim_text, claim.output


def test_healthy_control_show_has_no_invalid_warnings(
    runner, contract, tmp_path, monkeypatch
):
    root = _copy_case(tmp_path, contract["healthy_control"]["fixture"])
    monkeypatch.chdir(root)
    result = runner.invoke(cli, ["show", contract["healthy_control"]["task_id"]])
    assert result.exit_code == 0, result.output
    text = _normalize(result.output)
    for sub in contract["healthy_control"]["must_not_contain_substrings"]:
        assert _normalize(sub) not in text, result.output
    assert "Healthy task" in text
