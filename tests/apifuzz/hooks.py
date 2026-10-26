# SPDX-FileCopyrightText: The OpenArtifact Authors
#
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Schemathesis hooks for the OpenArtifact API fuzzing runs (scripts/api-fuzz.sh).

Loaded through SCHEMATHESIS_HOOKS. They register the project-specific checks;
the built-in checks and the exclusion list live in the runner and in
schemathesis.toml.
"""

import os

import schemathesis

# Operations that legitimately answer 2xx without credentials although the spec
# does not mark them public (`security: []`): reads of PUBLIC repositories.
# Every entry was confirmed against the real server by probing the seeded
# public repositories (seed-npm, seed-raw) and a private one (seed-npm-private,
# which answers 401). The check below additionally requires the {name}
# path parameter to be a public repository.
PUBLIC_REPO_READS = frozenset(
    {
        ("GET", "/api/v1/repository/{name}"),
        ("GET", "/api/v1/repositories/{name}/packages"),
        ("GET", "/api/v1/repositories/{name}/security/summary"),
        ("GET", "/api/v1/repositories/{name}/artifacts/{artifact_id}/properties"),
        ("GET", "/api/v1/repositories/{name}/artifacts/search"),
        ("GET", "/api/v1/repositories/{name}/packages/{id}"),
        ("GET", "/api/v1/repositories/{name}/packages/{id}/download"),
    }
)
PUBLIC_REPOS = frozenset(
    r for r in os.environ.get("OA_FUZZ_PUBLIC_REPOS", "seed-npm,seed-raw").split(",") if r
)

# Ids the run depends on (its users, their tokens, the permission targets that
# grant the roles). The fuzzer follows links from the list endpoints to real
# ids, so left alone it revokes its own token or deletes its own role and every
# later request answers 401/403. A request whose path names one of them is
# redirected to an id that does not exist: the destructive operations are still
# fuzzed, just never against the run's own credentials.
PROTECTED_IDS = frozenset(i for i in os.environ.get("OA_FUZZ_PROTECTED_IDS", "").split(",") if i)
UNKNOWN_ID = "00000000-0000-4000-8000-000000000000"


@schemathesis.hook
def before_call(context, case, kwargs):
    params = case.path_parameters
    if params:
        for name, value in params.items():
            if isinstance(value, str) and value in PROTECTED_IDS:
                params[name] = UNKNOWN_ID


# Operations that must not be reachable by a non-admin principal (the
# developer role has read/write/delete on repositories, never `manage`).
ADMIN_PREFIX = "/api/v1/admin/"


def _is_operation_call(response) -> bool:
    """False for CORS preflights: OPTIONS is answered by the CORS middleware
    before authentication (204, no body) and is not an API operation."""
    return response.request.method.upper() != "OPTIONS"


def _carries_credentials(response) -> bool:
    headers = response.request.headers
    return "Authorization" in headers or "Cookie" in headers


def _declared_public(case) -> bool:
    """True when the spec itself marks the operation `security: []`."""
    return case.operation.definition.raw.get("security") == []


@schemathesis.check
def anonymous_rejected(ctx, response, case):
    """A request without credentials must never succeed on a non-public route."""
    if not _is_operation_call(response) or _carries_credentials(response):
        return None
    if not 200 <= response.status_code < 300:
        return None
    if _declared_public(case):
        return None
    key = (case.method.upper(), case.operation.path)
    if key in PUBLIC_REPO_READS and case.path_parameters.get("name") in PUBLIC_REPOS:
        return None
    raise AssertionError(
        f"{case.method.upper()} {case.formatted_path} answered {response.status_code} "
        "without credentials; only operations the spec marks `security: []` and "
        "public-repository reads may do that"
    )


@schemathesis.check
def admin_forbidden_for_developer(ctx, response, case):
    """The developer principal must never succeed on /api/v1/admin/*."""
    if os.environ.get("OA_FUZZ_PRINCIPAL") != "developer":
        return None
    if not _is_operation_call(response):
        return None
    if case.operation.path.startswith(ADMIN_PREFIX) and 200 <= response.status_code < 300:
        raise AssertionError(
            f"{case.method.upper()} {case.formatted_path} answered {response.status_code} "
            "for a non-admin principal"
        )
    return None
