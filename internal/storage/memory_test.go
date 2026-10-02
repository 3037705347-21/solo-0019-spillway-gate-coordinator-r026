package storage

import (
	"context"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

func testNow() time.Time {
	return time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
}

func seedGateAndPendingCommands(t *testing.T, repo *MemoryRepository, targets []float64) (domain.Gate, []domain.GateCommand) {
	t.Helper()
	ctx := context.Background()
	now := testNow()

	gate, err := domain.NewGate("gate-test", "测试闸门", 10, 1.0, now)
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	if err := repo.CreateGate(ctx, gate); err != nil {
		t.Fatalf("create gate: %v", err)
	}

	var commands []domain.GateCommand
	for i, target := range targets {
		command, err := domain.NewGateCommand(
			"cmd-"+string(rune('a'+i)),
			gate.ID,
			target,
			gate.Aperture,
			gate.MaxAperture,
			"测试申请",
			"dispatcher",
			now,
		)
		if err != nil {
			t.Fatalf("new command: %v", err)
		}
		if err := repo.CreateCommand(ctx, command); err != nil {
			t.Fatalf("create command: %v", err)
		}
		commands = append(commands, command)
	}
	return gate, commands
}

func clearDecision() domain.InterlockDecision {
	decision, err := domain.NewInterlockDecision("clear", "safety-wu", "联锁满足", testNow())
	if err != nil {
		panic(err)
	}
	return decision
}

// 较早的申请被一条已执行完成的新动作超越：clear 复核必须拒绝，不能占用闸门，
// 也不能签发执行令牌。
func TestReviewCommand_StaleAfterPositionMoved(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0, 3.0})
	early, newer := commands[0], commands[1]
	now := testNow()

	// 新申请先复核并执行完成，闸门停在 3.0 后释放。
	approved, gate, err := repo.ReviewCommand(ctx, newer.ID, clearDecision(), "token-new", now)
	if err != nil {
		t.Fatalf("review newer command: %v", err)
	}
	if approved.Status != domain.CommandApproved || gate.ActiveCommandID != newer.ID {
		t.Fatalf("newer command should reserve the gate: status=%q active=%q",
			approved.Status, gate.ActiveCommandID)
	}
	if _, gate, err := repo.ExecuteCommand(ctx, newer.ID, "token-new", "operator", false, now); err != nil {
		t.Fatalf("execute newer command: %v", err)
	} else if gate.Aperture != 3.0 || gate.Status != domain.GateIdle {
		t.Fatalf("gate should settle idle at 3.0: status=%q aperture=%v", gate.Status, gate.Aperture)
	}

	// 迟到的旧申请（冻结起始开度 1.0，目标 2.0）此时通过 clear 复核。
	stale, gate, err := repo.ReviewCommand(ctx, early.ID, clearDecision(), "token-stale", now)
	if err != nil {
		t.Fatalf("review stale command: %v", err)
	}
	if stale.Status != domain.CommandRejected {
		t.Fatalf("stale command should be rejected, got %q", stale.Status)
	}
	if stale.ExecutionToken != "" {
		t.Fatalf("stale command must not receive an execution token, got %q", stale.ExecutionToken)
	}
	if gate.Status != domain.GateIdle || gate.ActiveCommandID != "" {
		t.Fatalf("stale review must not reserve the gate: status=%q active=%q",
			gate.Status, gate.ActiveCommandID)
	}
	if gate.Aperture != 3.0 {
		t.Fatalf("stale review must not move the gate, aperture=%v", gate.Aperture)
	}
	if stale.TerminalReason == "" {
		t.Fatal("rejected stale command should carry a terminal reason")
	}
}

// 目标开度已被现场达成：复核不应再次批准并制造一次多余的动作。
func TestReviewCommand_TargetAlreadySatisfied(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0, 3.0})
	early, newer := commands[0], commands[1]
	now := testNow()

	// 执行一条新动作，把闸门带到 3.0；再执行一条申请把闸门带回 2.0，
	// 恰好等于旧申请的目标开度。
	if _, _, err := repo.ReviewCommand(ctx, newer.ID, clearDecision(), "token-new", now); err != nil {
		t.Fatalf("review newer: %v", err)
	}
	if _, gate, err := repo.ExecuteCommand(ctx, newer.ID, "token-new", "operator", false, now); err != nil {
		t.Fatalf("execute newer: %v", err)
	} else if gate.Aperture != 3.0 {
		t.Fatalf("gate should settle at 3.0, got %v", gate.Aperture)
	}

	back, err := domain.NewGateCommand(
		"cmd-z", "gate-test", 2.0, 3.0, 10, "回到旧目标", "dispatcher", now)
	if err != nil {
		t.Fatalf("new back command: %v", err)
	}
	if err := repo.CreateCommand(ctx, back); err != nil {
		t.Fatalf("create back command: %v", err)
	}
	if _, _, err := repo.ReviewCommand(ctx, back.ID, clearDecision(), "token-back", now); err != nil {
		t.Fatalf("review back: %v", err)
	}
	if _, gate, err := repo.ExecuteCommand(ctx, back.ID, "token-back", "operator", false, now); err != nil {
		t.Fatalf("execute back: %v", err)
	} else if gate.Aperture != 2.0 || gate.Status != domain.GateIdle {
		t.Fatalf("gate should settle idle at 2.0: status=%q aperture=%v", gate.Status, gate.Aperture)
	}

	// 旧申请的目标 2.0 已在现场达成。
	satisfied, gate, err := repo.ReviewCommand(ctx, early.ID, clearDecision(), "token-sat", now)
	if err != nil {
		t.Fatalf("review satisfied command: %v", err)
	}
	if satisfied.Status != domain.CommandRejected {
		t.Fatalf("already-satisfied command should be rejected, got %q", satisfied.Status)
	}
	if satisfied.ExecutionToken != "" {
		t.Fatalf("satisfied command must not receive a token, got %q", satisfied.ExecutionToken)
	}
	if gate.Status != domain.GateIdle || gate.ActiveCommandID != "" {
		t.Fatalf("satisfied review must not reserve the gate: status=%q active=%q",
			gate.Status, gate.ActiveCommandID)
	}
	if gate.Aperture != 2.0 {
		t.Fatalf("satisfied review must not move the gate, aperture=%v", gate.Aperture)
	}
}

// 两条待复核申请几乎同时 clear：只有一个赢家，另一个得到显式冲突且保持 pending、
// 不签发令牌、闸门归属唯一。
func TestReviewCommand_ConcurrentClearSingleWinner(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0, 3.0})
	first, second := commands[0], commands[1]
	now := testNow()

	if _, _, err := repo.ReviewCommand(ctx, first.ID, clearDecision(), "token-1", now); err != nil {
		t.Fatalf("first review: %v", err)
	}
	if _, _, err := repo.ReviewCommand(ctx, second.ID, clearDecision(), "token-2", now); err == nil {
		t.Fatal("second concurrent clear review must return a conflict")
	} else if domain.ErrorKindOf(err) != domain.KindConflict {
		t.Fatalf("expected conflict error, got %v", err)
	}

	storedSecond, err := repo.Command(ctx, second.ID)
	if err != nil {
		t.Fatalf("read second command: %v", err)
	}
	if storedSecond.Status != domain.CommandPending {
		t.Fatalf("losing command must remain pending, got %q", storedSecond.Status)
	}
	if storedSecond.ExecutionToken != "" {
		t.Fatalf("losing command must not receive a token, got %q", storedSecond.ExecutionToken)
	}

	gate, err := repo.Gate(ctx, "gate-test")
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if gate.Status != domain.GateReserved || gate.ActiveCommandID != first.ID {
		t.Fatalf("gate must stay reserved by the winner: status=%q active=%q",
			gate.Status, gate.ActiveCommandID)
	}
}

// 等待时间长本身不构成失效：闸门位置未变时，晚到的 clear 复核照常通过。
func TestReviewCommand_LongWaitWithUnchangedPositionStillReady(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0})
	command := commands[0]

	later := testNow().Add(8 * time.Hour)
	approved, gate, err := repo.ReviewCommand(ctx, command.ID, clearDecision(), "token-late", later)
	if err != nil {
		t.Fatalf("late but valid review: %v", err)
	}
	if approved.Status != domain.CommandApproved || approved.ExecutionToken != "token-late" {
		t.Fatalf("late valid command should be approved with its token: status=%q token=%q",
			approved.Status, approved.ExecutionToken)
	}
	if gate.Status != domain.GateReserved || gate.ActiveCommandID != command.ID {
		t.Fatalf("gate should be reserved: status=%q active=%q", gate.Status, gate.ActiveCommandID)
	}
}

// 与位置无关的变化（另一条申请仍只是 pending）不影响本申请复核。
func TestReviewCommand_PendingSiblingDoesNotStale(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0, 3.0})
	first, second := commands[0], commands[1]
	now := testNow()

	approved, _, err := repo.ReviewCommand(ctx, first.ID, clearDecision(), "token-1", now)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if approved.Status != domain.CommandApproved {
		t.Fatalf("expected approved, got %q", approved.Status)
	}
	storedSecond, err := repo.Command(ctx, second.ID)
	if err != nil {
		t.Fatalf("read sibling: %v", err)
	}
	if storedSecond.Status != domain.CommandPending {
		t.Fatalf("untouched sibling should remain pending, got %q", storedSecond.Status)
	}
}

// blocked 复核保持原样：命令 rejected，不占用闸门。
func TestReviewCommand_BlockedUnchanged(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0})
	command := commands[0]
	now := testNow()

	blocked, err := domain.NewInterlockDecision("blocked", "safety-wu", "联锁不满足", now)
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	result, gate, err := repo.ReviewCommand(ctx, command.ID, blocked, "", now)
	if err != nil {
		t.Fatalf("blocked review: %v", err)
	}
	if result.Status != domain.CommandRejected || result.ExecutionToken != "" {
		t.Fatalf("blocked command rejected without token: status=%q token=%q",
			result.Status, result.ExecutionToken)
	}
	if gate.Status != domain.GateIdle || gate.ActiveCommandID != "" {
		t.Fatalf("blocked review must not reserve gate: status=%q active=%q",
			gate.Status, gate.ActiveCommandID)
	}
}

// 被判定过期的命令不能再凭任何令牌执行。
func TestExecuteCommand_RejectedStaleCannotExecute(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	_, commands := seedGateAndPendingCommands(t, repo, []float64{2.0, 3.0})
	early, newer := commands[0], commands[1]
	now := testNow()

	if _, _, err := repo.ReviewCommand(ctx, newer.ID, clearDecision(), "token-new", now); err != nil {
		t.Fatalf("review newer: %v", err)
	}
	if _, _, err := repo.ExecuteCommand(ctx, newer.ID, "token-new", "operator", false, now); err != nil {
		t.Fatalf("execute newer: %v", err)
	}
	stale, _, err := repo.ReviewCommand(ctx, early.ID, clearDecision(), "token-stale", now)
	if err != nil {
		t.Fatalf("review stale: %v", err)
	}
	if stale.Status != domain.CommandRejected {
		t.Fatalf("expected rejected, got %q", stale.Status)
	}
	if _, _, err := repo.ExecuteCommand(ctx, early.ID, "token-stale", "operator", false, now); err == nil {
		t.Fatal("rejected stale command must not be executable")
	}
}
