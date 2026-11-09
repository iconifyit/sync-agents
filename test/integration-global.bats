#!/usr/bin/env bats
#
# Integration tests for v0.3.0 commands: promote, global init/sync/status/clean.
#
# Isolation strategy: --global-root "$GLOBAL_ROOT" sets the global .agents/ dir.
# Per-tool dirs (claude, codeium, cursor, etc.) are derived from GLOBAL_ROOT's
# parent ($TEST_DIR), so nothing touches the real ~/.claude/ or ~/.codeium/.
#
# Run: npx bats test/integration-global.bats
# Or:  make test  (runs all bats in test/)

_REPO_ROOT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
SCRIPT="${SYNC_AGENTS_BIN:-$_REPO_ROOT/bin/sync-agents}"
PACKAGE_VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$_REPO_ROOT/package.json" | head -1)"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

setup() {
  TEST_DIR="$(mktemp -d)"
  GLOBAL_ROOT="$TEST_DIR/.agents"   # per-tool dirs live under $TEST_DIR

  # Project workspace (for promote tests that read from project .agents/)
  PROJECT_DIR="$TEST_DIR/project"
  mkdir -p "$PROJECT_DIR"
  git init --quiet "$PROJECT_DIR"

  # Derived tool dirs (global scope, parent = $TEST_DIR)
  CLAUDE_DIR="$TEST_DIR/.claude"
  CODEIUM_DIR="$TEST_DIR/.codeium/windsurf"
  CURSOR_DIR="$TEST_DIR/.cursor"
  COPILOT_DIR="$TEST_DIR/.github/copilot"
  CODEX_DIR="$TEST_DIR/.codex"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Seed a project-level .agents/ so promote tests have source artifacts.
init_project() {
  "$SCRIPT" init -d "$PROJECT_DIR"
}

# Write a passive rule to the global root (rules default to passive).
make_global_passive_rule() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/rules"
  cat > "$GLOBAL_ROOT/rules/$name.md" <<EOF
---
invocable: false
---
# $name
Passive rule content for $name.
EOF
}

# Write an invocable rule to the global root.
make_global_invocable_rule() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/rules"
  cat > "$GLOBAL_ROOT/rules/$name.md" <<EOF
---
invocable: true
---
# $name
Invocable rule content for $name.
EOF
}

# Write a single-file invocable skill to the global root.
make_global_single_skill() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/skills/$name"
  cat > "$GLOBAL_ROOT/skills/$name/SKILL.md" <<EOF
---
invocable: true
---
# $name Skill
Skill content for $name.
EOF
}

# Write a multi-file invocable skill to the global root (will be SKIP for Windsurf).
make_global_multi_skill() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/skills/$name"
  cat > "$GLOBAL_ROOT/skills/$name/SKILL.md" <<EOF
---
invocable: true
---
# $name Skill
EOF
  echo "# helper" > "$GLOBAL_ROOT/skills/$name/helper.sh"
}

# Write an invocable workflow to the global root.
make_global_invocable_workflow() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/workflows"
  cat > "$GLOBAL_ROOT/workflows/$name.md" <<EOF
---
invocable: true
---
# $name Workflow
Invocable workflow content.
EOF
}

# Write a passive workflow to the global root.
make_global_passive_workflow() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/workflows"
  cat > "$GLOBAL_ROOT/workflows/$name.md" <<EOF
---
invocable: false
---
# $name Workflow
Passive workflow content.
EOF
}

# ---------------------------------------------------------------------------
# global init
# ---------------------------------------------------------------------------

@test "global init creates .agents/ skeleton at --global-root" {
  run "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -d "$GLOBAL_ROOT/rules" ]
  [ -d "$GLOBAL_ROOT/skills" ]
  [ -d "$GLOBAL_ROOT/workflows" ]
  [ -f "$GLOBAL_ROOT/config" ]
}

@test "global init config file contains global target list" {
  run "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  grep -q "targets" "$GLOBAL_ROOT/config"
  grep -q "claude" "$GLOBAL_ROOT/config"
  grep -q "cursor" "$GLOBAL_ROOT/config"
}

@test "global init is idempotent" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  echo "custom = value" >> "$GLOBAL_ROOT/config"
  run "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  # Custom config line must survive
  grep -q "custom = value" "$GLOBAL_ROOT/config"
}

@test "global init --dry-run does not create directories" {
  run "$SCRIPT" global init --global-root "$GLOBAL_ROOT" --dry-run
  [ "$status" -eq 0 ]
  [ ! -d "$GLOBAL_ROOT" ]
}

@test "global init respects SYNC_AGENTS_GLOBAL_ROOT env var" {
  SYNC_AGENTS_GLOBAL_ROOT="$GLOBAL_ROOT" run "$SCRIPT" global init
  [ "$status" -eq 0 ]
  [ -d "$GLOBAL_ROOT/rules" ]
}

# ---------------------------------------------------------------------------
# promote — canonical form
# ---------------------------------------------------------------------------

@test "promote rule copies rule to global root" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule my-rule -d "$PROJECT_DIR"

  run "$SCRIPT" promote rule my-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/rules/my-rule.md" ]
  diff "$PROJECT_DIR/.agents/rules/my-rule.md" "$GLOBAL_ROOT/rules/my-rule.md"
}

@test "promote skill copies skill directory to global root" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add skill my-skill -d "$PROJECT_DIR"

  run "$SCRIPT" promote skill my-skill -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/skills/my-skill/SKILL.md" ]
}

@test "promote workflow copies workflow to global root" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add workflow my-flow -d "$PROJECT_DIR"

  run "$SCRIPT" promote workflow my-flow -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/workflows/my-flow.md" ]
}

@test "promote fails when destination exists and no --force" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule dup-rule -d "$PROJECT_DIR"
  "$SCRIPT" promote rule dup-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"

  run "$SCRIPT" promote rule dup-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"already exists"* ]] || [[ "$output" == *"--force"* ]]
}

@test "promote --force overwrites existing destination" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule dup-rule -d "$PROJECT_DIR"
  "$SCRIPT" promote rule dup-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"

  echo "# Updated" >> "$PROJECT_DIR/.agents/rules/dup-rule.md"
  run "$SCRIPT" promote rule dup-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT" --force
  [ "$status" -eq 0 ]
  diff "$PROJECT_DIR/.agents/rules/dup-rule.md" "$GLOBAL_ROOT/rules/dup-rule.md"
}

@test "promote --dry-run does not copy file" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule dry-rule -d "$PROJECT_DIR"

  run "$SCRIPT" promote rule dry-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT" --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"dry-run"* ]] || [[ "$output" == *"would"* ]]
  [ ! -f "$GLOBAL_ROOT/rules/dry-rule.md" ]
}

@test "promote fails for unknown type" {
  init_project
  run "$SCRIPT" promote badtype foo -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"unknown type"* ]]
}

@test "promote fails when source artifact does not exist" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  run "$SCRIPT" promote rule no-such-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not found"* ]]
}

@test "promote accepts plural type aliases" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule alias-rule -d "$PROJECT_DIR"
  run "$SCRIPT" promote rules alias-rule -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/rules/alias-rule.md" ]
}

# ---------------------------------------------------------------------------
# promote — path form
# ---------------------------------------------------------------------------

@test "promote path form auto-detects rule type" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule path-rule -d "$PROJECT_DIR"

  # Path form expects a path relative to project root; run from $PROJECT_DIR.
  run bash -c "cd '$PROJECT_DIR' && '$SCRIPT' promote .agents/rules/path-rule.md \
    --global-root '$GLOBAL_ROOT'"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/rules/path-rule.md" ]
}

@test "promote path form auto-detects skill type" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add skill path-skill -d "$PROJECT_DIR"

  run bash -c "cd '$PROJECT_DIR' && '$SCRIPT' promote .agents/skills/path-skill \
    --global-root '$GLOBAL_ROOT'"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/skills/path-skill/SKILL.md" ]
}

@test "promote path outside .agents/ is an error" {
  init_project
  run bash -c "cd '$PROJECT_DIR' && '$SCRIPT' promote /tmp/outside.md \
    --global-root '$GLOBAL_ROOT'"
  [ "$status" -ne 0 ]
}

# ---------------------------------------------------------------------------
# promote --sync composite
# ---------------------------------------------------------------------------

@test "promote --sync promotes and then runs global sync" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add rule sync-rule -d "$PROJECT_DIR"
  # Write frontmatter so it's passive → goes to claude/rules/
  cat > "$PROJECT_DIR/.agents/rules/sync-rule.md" <<'EOF'
---
invocable: false
---
# sync-rule
Content.
EOF

  run "$SCRIPT" promote rule sync-rule -d "$PROJECT_DIR" \
    --global-root "$GLOBAL_ROOT" --sync --sync-targets claude
  [ "$status" -eq 0 ]
  # Global root has the copy
  [ -f "$GLOBAL_ROOT/rules/sync-rule.md" ]
  # Claude symlink created by the embedded global sync
  [ -L "$CLAUDE_DIR/rules/sync-rule.md" ]
}

# ---------------------------------------------------------------------------
# global sync — Claude routing
# ---------------------------------------------------------------------------

@test "global sync: passive rule symlinked to claude/rules/" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "sec-rule"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/rules/sec-rule.md" ]
  # Symlink resolves to the global root source
  [[ "$(readlink "$CLAUDE_DIR/rules/sec-rule.md")" == *"$GLOBAL_ROOT"* ]]
}

@test "global sync: invocable rule symlinked to claude/commands/" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_invocable_rule "deploy-rule"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/commands/deploy-rule.md" ]
}

@test "global sync: invocable skill directory symlinked to claude/skills/<name>" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_single_skill "code-review"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  # Claude links the whole skill directory, so SKILL.md and any
  # supporting files are reachable through it.
  [ -L "$CLAUDE_DIR/skills/code-review" ]
  [ "$(readlink "$CLAUDE_DIR/skills/code-review")" == "$GLOBAL_ROOT/skills/code-review" ]
  [ -f "$CLAUDE_DIR/skills/code-review/SKILL.md" ]
}

@test "global sync: invocable workflow symlinked to claude/commands/" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_invocable_workflow "deploy-flow"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/commands/deploy-flow.md" ]
}

@test "global sync: passive skill to claude is skipped (no single-file destination)" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  mkdir -p "$GLOBAL_ROOT/skills/passive-skill"
  cat > "$GLOBAL_ROOT/skills/passive-skill/SKILL.md" <<'EOF'
---
invocable: false
---
# Passive Skill
EOF

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  # No symlink created for passive skill in claude
  [ ! -e "$CLAUDE_DIR/skills/passive-skill" ]
  [ ! -e "$CLAUDE_DIR/rules/passive-skill.md" ]
}

# ---------------------------------------------------------------------------
# global sync — Codeium routing
# ---------------------------------------------------------------------------

@test "global sync: passive rule concatenated to codeium/windsurf/memories/global_rules.md" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "no-secrets"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets codeium
  [ "$status" -eq 0 ]
  [ -f "$CODEIUM_DIR/memories/global_rules.md" ]
  grep -q "Generated by sync-agents" "$CODEIUM_DIR/memories/global_rules.md"
  grep -q "no-secrets" "$CODEIUM_DIR/memories/global_rules.md"
}

@test "global sync: invocable single-file workflow symlinked to codeium/windsurf/global_workflows/" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_invocable_workflow "deploy-flow"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets codeium
  [ "$status" -eq 0 ]
  [ -L "$CODEIUM_DIR/global_workflows/deploy-flow.md" ]
}

@test "global sync: multi-file skill is skipped for codeium" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_multi_skill "big-skill"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets codeium
  [ "$status" -eq 0 ]
  [ ! -e "$CODEIUM_DIR/global_workflows/big-skill.md" ]
  [[ "$output" == *"skip"* ]] || [[ "$output" == *"Windsurf"* ]]
}

# ---------------------------------------------------------------------------
# global sync — Cursor, Copilot, Codex routing
# ---------------------------------------------------------------------------

@test "global sync: all artifacts go to cursor/rules/ regardless of semantic" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "passive-r"
  make_global_invocable_rule "invoc-r"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets cursor
  [ "$status" -eq 0 ]
  [ -L "$CURSOR_DIR/rules/passive-r.md" ]
  [ -L "$CURSOR_DIR/rules/invoc-r.md" ]
}

@test "global sync: artifacts concatenated into copilot instructions.md" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "sec-rule"
  make_global_invocable_workflow "deploy-flow"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  [ -f "$COPILOT_DIR/instructions.md" ]
  grep -q "Generated by sync-agents" "$COPILOT_DIR/instructions.md"
  grep -q "sec-rule\|deploy-flow" "$COPILOT_DIR/instructions.md"
}

@test "global sync: artifacts concatenated into codex instructions.md" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "my-rule"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets codex
  [ "$status" -eq 0 ]
  [ -f "$CODEX_DIR/instructions.md" ]
  grep -q "Generated by sync-agents" "$CODEX_DIR/instructions.md"
}

# ---------------------------------------------------------------------------
# global sync — flags and contracts
# ---------------------------------------------------------------------------

@test "global sync --dry-run makes no filesystem changes" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "dry-rule"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"dry-run"* ]] || [[ "$output" == *"would"* ]]
  [ ! -e "$CLAUDE_DIR" ]
  [ ! -e "$CURSOR_DIR" ]
}

@test "global sync --targets filters to specified tools only" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "filtered-rule"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/rules/filtered-rule.md" ]
  [ ! -e "$CURSOR_DIR" ]
  [ ! -f "$COPILOT_DIR/instructions.md" ]
}

@test "global sync is idempotent — second run does not rewrite unchanged concat" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "idem-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  # Second run must report "already current", NOT "regenerated".
  # This directly asserts idempotency instead of relying on mtime
  # comparison which can be non-deterministic across CI filesystems.
  [[ "$output" == *"already current"* ]]
}

@test "global sync repairs drifted symlink on next run" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "drift-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude

  # Deliberately drift the symlink
  ln -sfn /tmp/wrong "$CLAUDE_DIR/rules/drift-rule.md"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [[ "$(readlink "$CLAUDE_DIR/rules/drift-rule.md")" == *"$GLOBAL_ROOT"* ]]
}

@test "global sync fails when global root does not exist" {
  run "$SCRIPT" global sync --global-root "$TEST_DIR/.no-such-root"
  [ "$status" -ne 0 ]
  [[ "$output" == *"does not exist"* ]] || [[ "$output" == *"global init"* ]]
}

@test "global sync: frontmatter stripped from concat entries" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "strip-rule"

  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot
  # concat file must NOT contain raw YAML frontmatter delimiters
  run grep -c "^---$" "$COPILOT_DIR/instructions.md"
  [ "$output" -eq 0 ]
}

# ---------------------------------------------------------------------------
# global status
# ---------------------------------------------------------------------------

@test "global status shows [synced] for a correctly linked artifact" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "my-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"synced"* ]]
  [[ "$output" == *"my-rule"* ]]
}

@test "global status shows [missing] when symlink absent" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "miss-rule"
  # Do NOT sync — symlink will be absent

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"missing"* ]]
}

@test "global status shows [drifted] for wrong-target symlink" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "drift-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  ln -sfn /tmp/wrong "$CLAUDE_DIR/rules/drift-rule.md"

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"drifted"* ]]
}

@test "global status shows concat ok for current concat file" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "concat-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  [[ "$output" == *"concat ok"* ]] || [[ "$output" == *"ok"* ]]
}

@test "global status shows concat missing when file absent" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "no-sync-rule"
  # Do NOT sync

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  [[ "$output" == *"missing"* ]]
}

@test "global status --targets filters output to specified tools" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "any-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT"

  run "$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [[ "$output" == *"claude"* ]]
  # codex should not appear in filtered output
  [[ "$output" != *"codex"* ]]
}

@test "global status errors when global root does not exist" {
  run "$SCRIPT" global status --global-root "$TEST_DIR/.no-root"
  [ "$status" -ne 0 ]
}

# ---------------------------------------------------------------------------
# global clean
# ---------------------------------------------------------------------------

@test "global clean removes sync-agents symlinks from tool dirs" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "clean-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ -L "$CLAUDE_DIR/rules/clean-rule.md" ]

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ ! -e "$CLAUDE_DIR/rules/clean-rule.md" ]
}

@test "global clean removes sync-agents-owned concat files" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "rule-for-clean"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot
  [ -f "$COPILOT_DIR/instructions.md" ]
  grep -q "Generated by sync-agents" "$COPILOT_DIR/instructions.md"

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  [ ! -f "$COPILOT_DIR/instructions.md" ]
}

@test "global clean does NOT remove user-owned files (no banner)" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "owned-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets copilot

  # Replace the concat file with user-owned content (no banner)
  echo "# My custom instructions" > "$COPILOT_DIR/instructions.md"

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets copilot
  [ "$status" -eq 0 ]
  # File must still exist — it's user-owned
  [ -f "$COPILOT_DIR/instructions.md" ]
  grep -q "My custom instructions" "$COPILOT_DIR/instructions.md"
}

@test "global clean does NOT remove user-owned symlinks (outside global root)" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  mkdir -p "$CLAUDE_DIR/rules"
  ln -sfn /tmp/user-rule.md "$CLAUDE_DIR/rules/user-rule.md"

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/rules/user-rule.md" ]
}

@test "global clean --dry-run makes no changes" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "preserve-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"dry-run"* ]] || [[ "$output" == *"would"* ]]
  [ -L "$CLAUDE_DIR/rules/preserve-rule.md" ]
}

@test "global clean --targets filters to specified tools only" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "multi-tool-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude,cursor

  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets cursor
  [ "$status" -eq 0 ]
  [ ! -e "$CURSOR_DIR/rules/multi-tool-rule.md" ]
  # Claude symlink must still exist (not cleaned)
  [ -L "$CLAUDE_DIR/rules/multi-tool-rule.md" ]
}

@test "global clean is idempotent" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "idem-clean"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude

  "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude
  run "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
}

@test "global clean does not remove .agents/ source tree" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_passive_rule "src-rule"
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  "$SCRIPT" global clean --global-root "$GLOBAL_ROOT"

  [ -f "$GLOBAL_ROOT/rules/src-rule.md" ]
}

@test "global clean errors when global root does not exist" {
  run "$SCRIPT" global clean --global-root "$TEST_DIR/.no-root"
  [ "$status" -ne 0 ]
}

# ---------------------------------------------------------------------------
# SYNC_AGENTS_GLOBAL_ROOT environment variable
# ---------------------------------------------------------------------------

@test "SYNC_AGENTS_GLOBAL_ROOT env var used when --global-root not set" {
  SYNC_AGENTS_GLOBAL_ROOT="$GLOBAL_ROOT" run "$SCRIPT" global init
  [ "$status" -eq 0 ]
  [ -d "$GLOBAL_ROOT/rules" ]
}

@test "--global-root flag takes precedence over SYNC_AGENTS_GLOBAL_ROOT" {
  OTHER_ROOT="$TEST_DIR/.other-agents"
  SYNC_AGENTS_GLOBAL_ROOT="$OTHER_ROOT" \
    run "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -d "$GLOBAL_ROOT/rules" ]
  [ ! -d "$OTHER_ROOT" ]
}

# ---------------------------------------------------------------------------
# Full round-trip: init → promote → global sync → global status → global clean
# ---------------------------------------------------------------------------

@test "full round-trip: project rule promoted, synced, verified, and cleaned" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"

  # Create passive project rule
  cat > "$PROJECT_DIR/.agents/rules/rtt-rule.md" <<'EOF'
---
invocable: false
---
# RTT Rule
Round-trip test content.
EOF

  # Promote to global
  "$SCRIPT" promote rule rtt-rule \
    -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ -f "$GLOBAL_ROOT/rules/rtt-rule.md" ]

  # Sync to Claude
  "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ -L "$CLAUDE_DIR/rules/rtt-rule.md" ]

  # Status must report synced
  status_out="$("$SCRIPT" global status --global-root "$GLOBAL_ROOT" --targets claude)"
  [[ "$status_out" == *"synced"* ]]
  [[ "$status_out" == *"rtt-rule"* ]]

  # Clean removes symlink
  "$SCRIPT" global clean --global-root "$GLOBAL_ROOT" --targets claude
  [ ! -e "$CLAUDE_DIR/rules/rtt-rule.md" ]

  # Source must still be in global root
  [ -f "$GLOBAL_ROOT/rules/rtt-rule.md" ]
}

# ---------------------------------------------------------------------------
# agents bucket (SPEC-004 Part B)
# ---------------------------------------------------------------------------

make_global_agent() {
  local name="$1"
  mkdir -p "$GLOBAL_ROOT/agents"
  cat > "$GLOBAL_ROOT/agents/$name.md" <<AGENTEOF
---
name: $name
description: Reviews code. Use when a review is requested.
---
You are the $name subagent.
AGENTEOF
}

@test "promote agent copies agent to global root" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add agent my-reviewer -d "$PROJECT_DIR"

  run "$SCRIPT" promote agent my-reviewer -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/agents/my-reviewer.md" ]
}

@test "global sync: agent symlinked to claude/agents/" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_agent "reviewer"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/agents/reviewer.md" ]
  [[ "$(readlink "$CLAUDE_DIR/agents/reviewer.md")" == *"$GLOBAL_ROOT"* ]]
}

@test "global sync: agent is skipped for non-claude tools" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  make_global_agent "reviewer"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets cursor,codex
  [ "$status" -eq 0 ]
  [[ "$output" == *"no subagent surface"* ]]
  [ ! -e "$CURSOR_DIR/rules/reviewer.md" ]
  if [ -f "$CODEX_DIR/instructions.md" ]; then
    ! grep -q "reviewer" "$CODEX_DIR/instructions.md"
  fi
}

# ---------------------------------------------------------------------------
# plans + specs buckets (SPEC-004 Part D)
# ---------------------------------------------------------------------------

@test "global sync: plan symlinked to claude/plans/, skipped elsewhere" {
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  mkdir -p "$GLOBAL_ROOT/plans"
  printf -- '---\nname: roadmap\ndescription: Roadmap. Use when planning.\n---\n# Roadmap\n' > "$GLOBAL_ROOT/plans/roadmap.md"

  run "$SCRIPT" global sync --global-root "$GLOBAL_ROOT" --targets claude,cursor
  [ "$status" -eq 0 ]
  [ -L "$CLAUDE_DIR/plans/roadmap.md" ]
  [ ! -e "$CURSOR_DIR/rules/roadmap.md" ]
}

@test "promote spec copies spec to global root" {
  init_project
  "$SCRIPT" global init --global-root "$GLOBAL_ROOT"
  "$SCRIPT" add spec my-spec -d "$PROJECT_DIR"

  run "$SCRIPT" promote spec my-spec -d "$PROJECT_DIR" --global-root "$GLOBAL_ROOT"
  [ "$status" -eq 0 ]
  [ -f "$GLOBAL_ROOT/specs/my-spec.md" ]
}
