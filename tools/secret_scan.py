#!/usr/bin/env python3
"""Lightweight repository secret scan for release checks."""

from __future__ import annotations

import re
import stat
import subprocess
import sys
from pathlib import Path, PurePosixPath
from typing import Iterable


ROOT = Path(__file__).resolve().parents[1]
MAX_FILE_SIZE = 2 * 1024 * 1024

SKIP_DIRS = {
    ".git",
    ".playwright-cli",
    "dist",
    "node_modules",
    "__pycache__",
}

TEXT_SUFFIXES = {
    ".go",
    ".ts",
    ".tsx",
    ".vue",
    ".js",
    ".mjs",
    ".cjs",
    ".json",
    ".yaml",
    ".yml",
    ".toml",
    ".env",
    ".example",
    ".md",
    ".sh",
    ".service",
    ".sql",
    ".txt",
    ".py",
    ".ini",
    ".conf",
    ".cfg",
    ".properties",
    ".tpl",
    ".pem",
    ".key",
    ".p8",
}

SPECIAL_TEXT_NAMES = {
    "Dockerfile",
    "Dockerfile.goreleaser",
    "Makefile",
}

SECRET_PATTERNS = [
    ("openai_project_key", re.compile(r"\bsk-proj-[A-Za-z0-9_-]{80,}\b")),
    ("openai_api_key", re.compile(r"\bsk-(?!test-|usage-|getby|update-|reuse-)[A-Za-z0-9_-]{40,}\b")),
    ("anthropic_api_key", re.compile(r"\bsk-ant-[A-Za-z0-9_-]{30,}\b")),
    ("google_api_key", re.compile(r"\bAIza[0-9A-Za-z_-]{20,}\b")),
    ("google_oauth_client_secret", re.compile(r"\bGOCSPX-[0-9A-Za-z_-]{24,}\b")),
    ("aws_access_key", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
]

FIELD_SECRET_PATTERN = re.compile(
    r"""
    (?<![A-Za-z0-9_])
    (?P<field>(?:[A-Za-z0-9]+_)*(?:client_secret|app_secret|refresh_token|access_token))
    (?![A-Za-z0-9_])
    ["']?\s*(?:=|:|=>)\s*
    (?:
        ["'](?P<quoted>[^"'\r\n]{24,})["']
        |
        (?P<bare>[A-Za-z0-9][A-Za-z0-9._~+/=-]{23,})
    )
    """,
    re.IGNORECASE | re.VERBOSE,
)

PRIVATE_KEY_BLOCK_PATTERN = re.compile(
    r"-----BEGIN (?P<kind>RSA |OPENSSH |EC |)PRIVATE KEY-----"
    r"(?P<body>.*?)"
    r"-----END (?P=kind)PRIVATE KEY-----",
    re.DOTALL,
)
PRIVATE_KEY_BODY_PATTERN = re.compile(r"[A-Za-z0-9+/=]+\Z")

TOKEN_VALUE_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._~+/=-]*\Z")
CODE_REFERENCE_PATTERN = re.compile(
    r"(?:form|config|cfg|settings|opts|options|request|req|response|resp|"
    r"credentials|credential|oauth)"
    r"\.(?:[A-Za-z_][A-Za-z0-9_]*)"
    r"(?:\.[A-Za-z_][A-Za-z0-9_]*)*\Z",
    re.IGNORECASE,
)
TEMPLATE_VALUE_PATTERNS = (
    re.compile(r"\$\{[A-Za-z_][A-Za-z0-9_]*\}\Z"),
    re.compile(r"\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}\Z"),
    re.compile(r"<[A-Za-z_][A-Za-z0-9_.-]*>\Z"),
)

PLACEHOLDER_EXACT = {
    "...",
    "abc",
    "change-this",
    "changeme",
    "dummy",
    "example",
    "mock",
    "placeholder",
    "replace-me",
    "sample",
    "test",
    "your-secret-here",
}

PLACEHOLDER_MARKERS = {
    "abc",
    "change",
    "dummy",
    "example",
    "fake",
    "mock",
    "placeholder",
    "replace",
    "sample",
    "test",
    "your",
}

PLACEHOLDER_WORDS = PLACEHOLDER_MARKERS | {
    "access",
    "app",
    "built",
    "client",
    "credential",
    "here",
    "in",
    "key",
    "local",
    "me",
    "oauth",
    "refresh",
    "secret",
    "this",
    "token",
    "value",
}

KNOWN_SECRET_PREFIXES = (
    "sk-proj-",
    "sk-ant-",
    "sk-",
    "gocspx-",
    "aiza",
    "akia",
)


class ScanFailure(RuntimeError):
    """Raised when Git-backed repository content cannot be inspected safely."""


def run_git(arguments: list[str], *, input_bytes: bytes | None = None) -> bytes:
    try:
        result = subprocess.run(
            ["git", *arguments],
            cwd=ROOT,
            input=input_bytes,
            check=True,
            capture_output=True,
        )
    except OSError as error:
        raise ScanFailure(f"unable to run git {' '.join(arguments)}: {error}") from error
    except subprocess.CalledProcessError as error:
        stderr = error.stderr.decode("utf-8", errors="replace").strip()
        detail = f": {stderr}" if stderr else ""
        raise ScanFailure(f"git {' '.join(arguments)} failed{detail}") from error
    return result.stdout


def decode_git_paths(raw: bytes) -> list[str]:
    try:
        return [item.decode("utf-8") for item in raw.split(b"\0") if item]
    except UnicodeDecodeError as error:
        raise ScanFailure(f"git returned a non-UTF-8 repository path: {error}") from error


def normalize_repository_path(relative_path: str) -> str:
    pure_path = PurePosixPath(relative_path.replace("\\", "/"))
    if pure_path.is_absolute() or not pure_path.parts or ".." in pure_path.parts:
        raise ScanFailure(f"git returned an unsafe repository path: {relative_path!r}")
    return pure_path.as_posix()


def is_text_path(relative_path: str) -> bool:
    pure_path = PurePosixPath(relative_path)
    if any(part in SKIP_DIRS for part in pure_path.parts[:-1]):
        return False

    name = pure_path.name
    lower_name = name.lower()
    if name in SPECIAL_TEXT_NAMES:
        return True
    if lower_name == ".env" or lower_name.startswith(".env.") or lower_name.endswith(".env"):
        return True
    return pure_path.suffix.lower() in TEXT_SUFFIXES


def strip_known_secret_prefix(value: str) -> str:
    lower = value.lower()
    for prefix in KNOWN_SECRET_PREFIXES:
        if lower.startswith(prefix):
            return value[len(prefix) :]
    return value


def is_repeated_filler(value: str) -> bool:
    if len(value) < 4:
        return False
    if len(set(value.lower())) == 1:
        return True
    for unit_length in range(1, min(8, len(value) // 3) + 1):
        if len(value) % unit_length == 0:
            unit = value[:unit_length]
            if unit * (len(value) // unit_length) == value:
                return True
    return False


def is_placeholder_value(value: str) -> bool:
    """Allow only values whose entire matched value is recognizably a placeholder."""

    candidate = value.strip().strip('"\'').strip()
    if not candidate:
        return True

    lower = candidate.lower()
    if lower in PLACEHOLDER_EXACT:
        return True
    if any(pattern.fullmatch(candidate) for pattern in TEMPLATE_VALUE_PATTERNS):
        return True

    payload = strip_known_secret_prefix(candidate).strip("-_.")
    if is_repeated_filler(payload):
        return True

    words = [word for word in re.split(r"[^a-z0-9]+", payload.lower()) if word]
    if words and any(word in PLACEHOLDER_MARKERS for word in words):
        return all(word in PLACEHOLDER_WORDS or word.isdigit() for word in words)
    return False


def is_suspicious_field_value(value: str, *, allow_code_reference: bool = False) -> bool:
    candidate = value.strip()
    if len(candidate) < 24 or len(candidate) > 4096:
        return False
    if is_placeholder_value(candidate):
        return False
    if allow_code_reference and CODE_REFERENCE_PATTERN.fullmatch(candidate):
        return False
    if not TOKEN_VALUE_PATTERN.fullmatch(candidate):
        return False

    return len(set(candidate.lower())) >= 10


def decode_text(data: bytes, source: str) -> tuple[str | None, str | None]:
    if len(data) > MAX_FILE_SIZE:
        return None, f"{source}: exceeds {MAX_FILE_SIZE} byte scan limit"
    if b"\0" in data:
        return None, f"{source}: eligible text file contains NUL bytes"
    try:
        return data.decode("utf-8-sig"), None
    except UnicodeDecodeError as error:
        return None, f"{source}: unable to decode as UTF-8: {error}"


def private_key_body_is_suspicious(body: str) -> bool:
    compact = body.replace("\\n", "").replace("\\r", "")
    compact = "".join(compact.split())
    if len(compact) < 80 or not PRIVATE_KEY_BODY_PATTERN.fullmatch(compact):
        return False
    return not is_placeholder_value(compact)


def scan_text(text: str, relative_path: str, source: str) -> list[str]:
    findings: list[str] = []

    for match in PRIVATE_KEY_BLOCK_PATTERN.finditer(text):
        if private_key_body_is_suspicious(match.group("body")):
            line_no = text.count("\n", 0, match.start()) + 1
            findings.append(f"[{source}] {relative_path}:{line_no}: possible private_key")

    for line_no, line in enumerate(text.splitlines(), 1):
        occupied_spans: list[tuple[int, int]] = []
        for name, pattern in SECRET_PATTERNS:
            for match in pattern.finditer(line):
                matched_value = match.group(0)
                if is_placeholder_value(matched_value):
                    continue
                findings.append(f"[{source}] {relative_path}:{line_no}: possible {name}")
                occupied_spans.append(match.span())

        for match in FIELD_SECRET_PATTERN.finditer(line):
            value = match.group("quoted") or match.group("bare") or ""
            value_span = match.span("quoted") if match.group("quoted") is not None else match.span("bare")
            if any(start < value_span[1] and value_span[0] < end for start, end in occupied_spans):
                continue
            if is_suspicious_field_value(
                value,
                allow_code_reference=match.group("bare") is not None,
            ):
                field = match.group("field").lower()
                findings.append(f"[{source}] {relative_path}:{line_no}: possible {field}")
    return findings


def parse_index_entries(raw: bytes) -> list[tuple[str, str, str]]:
    entries: list[tuple[str, str, str]] = []
    for record in raw.split(b"\0"):
        if not record:
            continue
        try:
            metadata, encoded_path = record.split(b"\t", 1)
            mode, object_id, stage = metadata.decode("ascii").split()
            relative_path = normalize_repository_path(encoded_path.decode("utf-8"))
        except (UnicodeDecodeError, ValueError) as error:
            raise ScanFailure(f"unable to parse git index entry: {record!r}: {error}") from error
        entries.append((mode, object_id, stage + ":" + relative_path))
    return entries


def query_object_info(object_ids: Iterable[str]) -> dict[str, tuple[str, int]]:
    unique_ids = list(dict.fromkeys(object_ids))
    if not unique_ids:
        return {}
    request = ("\n".join(unique_ids) + "\n").encode("ascii")
    output = run_git(["cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)"], input_bytes=request)
    lines = output.decode("ascii", errors="strict").splitlines()
    if len(lines) != len(unique_ids):
        raise ScanFailure("git cat-file returned an unexpected number of object headers")

    info: dict[str, tuple[str, int]] = {}
    for expected_id, line in zip(unique_ids, lines):
        parts = line.split()
        if len(parts) != 3:
            raise ScanFailure(f"git cat-file returned an invalid object header: {line!r}")
        object_id, object_type, raw_size = parts
        try:
            size = int(raw_size)
        except ValueError as error:
            raise ScanFailure(f"git cat-file returned an invalid object size: {line!r}") from error
        info[expected_id] = (object_type, size)
        info[object_id] = (object_type, size)
    return info


def read_blobs(object_ids: Iterable[str]) -> dict[str, bytes]:
    unique_ids = list(dict.fromkeys(object_ids))
    if not unique_ids:
        return {}
    request = ("\n".join(unique_ids) + "\n").encode("ascii")
    output = run_git(["cat-file", "--batch"], input_bytes=request)

    blobs: dict[str, bytes] = {}
    cursor = 0
    for expected_id in unique_ids:
        header_end = output.find(b"\n", cursor)
        if header_end < 0:
            raise ScanFailure("git cat-file output ended before an object header")
        header = output[cursor:header_end].decode("ascii", errors="strict")
        parts = header.split()
        if len(parts) != 3 or parts[1] != "blob":
            raise ScanFailure(f"git cat-file returned an invalid blob header: {header!r}")
        object_id, _, raw_size = parts
        try:
            size = int(raw_size)
        except ValueError as error:
            raise ScanFailure(f"git cat-file returned an invalid blob size: {header!r}") from error
        content_start = header_end + 1
        content_end = content_start + size
        if content_end >= len(output) or output[content_end : content_end + 1] != b"\n":
            raise ScanFailure(f"git cat-file returned truncated content for {expected_id}")
        blobs[expected_id] = output[content_start:content_end]
        blobs[object_id] = output[content_start:content_end]
        cursor = content_end + 1
    if cursor != len(output):
        raise ScanFailure("git cat-file returned unexpected trailing data")
    return blobs


def scan_index() -> tuple[list[str], list[str]]:
    findings: list[str] = []
    errors: list[str] = []
    entries = parse_index_entries(run_git(["ls-files", "-z", "--stage"]))

    candidates: list[tuple[str, str, str, str]] = []
    for mode, object_id, staged_path in entries:
        stage, relative_path = staged_path.split(":", 1)
        if not is_text_path(relative_path):
            continue
        source = "index" if stage == "0" else f"index-stage-{stage}"
        if mode == "120000":
            errors.append(f"[{source}] {relative_path}: eligible path is a symbolic link")
            continue
        if mode == "160000":
            continue
        if mode not in {"100644", "100755"}:
            errors.append(f"[{source}] {relative_path}: unsupported index mode {mode}")
            continue
        candidates.append((object_id, relative_path, source, mode))

    object_info = query_object_info(object_id for object_id, _, _, _ in candidates)
    readable_ids: list[str] = []
    for object_id, relative_path, source, _ in candidates:
        object_type, size = object_info.get(object_id, ("missing", -1))
        if object_type != "blob":
            errors.append(f"[{source}] {relative_path}: index object is not a readable blob")
        elif size > MAX_FILE_SIZE:
            errors.append(f"[{source}] {relative_path}: exceeds {MAX_FILE_SIZE} byte scan limit")
        else:
            readable_ids.append(object_id)

    blobs = read_blobs(readable_ids)
    for object_id, relative_path, source, _ in candidates:
        data = blobs.get(object_id)
        if data is None:
            continue
        text, error = decode_text(data, f"[{source}] {relative_path}")
        if error:
            errors.append(error)
            continue
        findings.extend(scan_text(text or "", relative_path, source))
    return findings, errors


def scan_worktree() -> tuple[list[str], list[str]]:
    findings: list[str] = []
    errors: list[str] = []
    repository_paths = decode_git_paths(
        run_git(["ls-files", "-z", "--cached", "--others", "--exclude-standard"])
    )
    deleted_paths = set(decode_git_paths(run_git(["ls-files", "-z", "--deleted"])))

    for raw_relative_path in repository_paths:
        relative_path = normalize_repository_path(raw_relative_path)
        if not is_text_path(relative_path):
            continue
        path = ROOT.joinpath(*PurePosixPath(relative_path).parts)
        try:
            metadata = path.lstat()
        except FileNotFoundError:
            if raw_relative_path in deleted_paths or relative_path in deleted_paths:
                continue
            errors.append(f"[worktree] {relative_path}: disappeared before it could be scanned")
            continue
        except OSError as error:
            errors.append(f"[worktree] {relative_path}: unable to inspect: {error}")
            continue

        if stat.S_ISLNK(metadata.st_mode):
            errors.append(f"[worktree] {relative_path}: eligible path is a symbolic link")
            continue
        if not stat.S_ISREG(metadata.st_mode):
            errors.append(f"[worktree] {relative_path}: eligible path is not a regular file")
            continue
        if metadata.st_size > MAX_FILE_SIZE:
            errors.append(f"[worktree] {relative_path}: exceeds {MAX_FILE_SIZE} byte scan limit")
            continue

        try:
            with path.open("rb") as file_handle:
                data = file_handle.read(MAX_FILE_SIZE + 1)
        except OSError as error:
            errors.append(f"[worktree] {relative_path}: unable to read: {error}")
            continue

        text, error = decode_text(data, f"[worktree] {relative_path}")
        if error:
            errors.append(error)
            continue
        findings.extend(scan_text(text or "", relative_path, "worktree"))
    return findings, errors


def main() -> int:
    findings: list[str] = []
    scan_errors: list[str] = []

    try:
        index_findings, index_errors = scan_index()
        worktree_findings, worktree_errors = scan_worktree()
        findings.extend(index_findings)
        findings.extend(worktree_findings)
        scan_errors.extend(index_errors)
        scan_errors.extend(worktree_errors)
    except (OSError, ScanFailure, UnicodeDecodeError) as error:
        print(f"Secret scan could not enumerate repository content: {error}")
        return 1

    if scan_errors:
        print("Secret scan could not inspect every eligible repository entry:")
        for error in scan_errors:
            print(f"  {error}")

    if findings:
        print("Secret scan found possible credentials:")
        for finding in findings:
            print(f"  {finding}")

    if scan_errors or findings:
        return 1

    print("Secret scan passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
