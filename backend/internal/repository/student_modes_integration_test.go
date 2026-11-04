package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStudentModeSettersSkipUnchangedRowsAndRemainExclusive(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, false)
	repo := NewConversationRepository(pool)

	if err := repo.SetStudentChatMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("включить режим общения: %v", err)
	}
	state := readStudentModeState(t, ctx, pool, fixture.studentID)
	if !state.chatEnabled || state.lessonEnabled {
		t.Fatalf("после включения общения ожидался chat=true, lesson=false: %+v", state)
	}

	const sentinel = "2000-01-01 00:00:00+00"
	if _, err := pool.Exec(ctx, "UPDATE student_chat_modes SET updated_at = $2::timestamptz WHERE student_id = $1", fixture.studentID, sentinel); err != nil {
		t.Fatalf("подготовить updated_at режима общения: %v", err)
	}
	before := readStudentModeState(t, ctx, pool, fixture.studentID)
	if err := repo.SetStudentChatMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("повторно включить режим общения: %v", err)
	}
	after := readStudentModeState(t, ctx, pool, fixture.studentID)
	assertStudentModeStateUnchanged(t, before, after, "повторное включение режима общения")

	if err := repo.SetStudentLessonMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("включить режим урока: %v", err)
	}
	state = readStudentModeState(t, ctx, pool, fixture.studentID)
	if state.chatEnabled || !state.lessonEnabled {
		t.Fatalf("включение урока должно выключить общение: %+v", state)
	}

	if _, err := pool.Exec(ctx, "UPDATE student_chat_modes SET updated_at = $2::timestamptz WHERE student_id = $1", fixture.studentID, sentinel); err != nil {
		t.Fatalf("подготовить updated_at режима урока: %v", err)
	}
	before = readStudentModeState(t, ctx, pool, fixture.studentID)
	if err := repo.SetStudentLessonMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("повторно включить режим урока: %v", err)
	}
	after = readStudentModeState(t, ctx, pool, fixture.studentID)
	assertStudentModeStateUnchanged(t, before, after, "повторное включение режима урока")

	// Выключение уже выключенного chat mode не должно сбрасывать lesson mode.
	if err := repo.SetStudentChatMode(ctx, fixture.studentID, false); err != nil {
		t.Fatalf("повторно выключить режим общения: %v", err)
	}
	state = readStudentModeState(t, ctx, pool, fixture.studentID)
	if state.chatEnabled || !state.lessonEnabled {
		t.Fatalf("выключение общения не должно выключать режим урока: %+v", state)
	}

	if err := repo.SetStudentLessonMode(ctx, fixture.studentID, false); err != nil {
		t.Fatalf("выключить режим урока: %v", err)
	}
	state = readStudentModeState(t, ctx, pool, fixture.studentID)
	if state.chatEnabled || state.lessonEnabled {
		t.Fatalf("после выключения урока оба режима должны быть выключены: %+v", state)
	}

	// Выключение lesson mode не должно выключать активное общение.
	if err := repo.SetStudentChatMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("снова включить режим общения: %v", err)
	}
	if err := repo.SetStudentLessonMode(ctx, fixture.studentID, false); err != nil {
		t.Fatalf("выключить уже выключенный режим урока: %v", err)
	}
	state = readStudentModeState(t, ctx, pool, fixture.studentID)
	if !state.chatEnabled || state.lessonEnabled {
		t.Fatalf("выключение lesson mode не должно сбрасывать chat mode: %+v", state)
	}

	// Включение любого режима всегда выключает противоположный режим.
	if err := repo.SetStudentLessonMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("переключить на режим урока: %v", err)
	}
	if err := repo.SetStudentChatMode(ctx, fixture.studentID, true); err != nil {
		t.Fatalf("переключить обратно на режим общения: %v", err)
	}
	state = readStudentModeState(t, ctx, pool, fixture.studentID)
	if !state.chatEnabled || state.lessonEnabled {
		t.Fatalf("режимы не переключились взаимоисключающим образом: %+v", state)
	}
}

func TestStudentModesRemainExclusiveUnderConcurrentSwitches(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := createConversationFixture(t, pool, false)
	repo := NewConversationRepository(pool)

	for iteration := 0; iteration < 20; iteration++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			errs <- repo.SetStudentChatMode(ctx, fixture.studentID, true)
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- repo.SetStudentLessonMode(ctx, fixture.studentID, true)
		}()

		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("конкурентное переключение режимов (итерация %d): %v", iteration, err)
			}
		}

		state := readStudentModeState(t, ctx, pool, fixture.studentID)
		if state.chatEnabled && state.lessonEnabled {
			t.Fatalf("оба режима включены после конкурентного переключения (итерация %d): %+v", iteration, state)
		}
	}
}

type studentModeState struct {
	xmin          string
	chatEnabled   bool
	lessonEnabled bool
	updatedAt     time.Time
}

func readStudentModeState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, studentID int64) studentModeState {
	t.Helper()
	var state studentModeState
	if err := pool.QueryRow(ctx, `
		SELECT xmin::text, chat_mode_enabled, lesson_mode_enabled, updated_at
		FROM student_chat_modes
		WHERE student_id = $1
	`, studentID).Scan(&state.xmin, &state.chatEnabled, &state.lessonEnabled, &state.updatedAt); err != nil {
		t.Fatalf("прочитать состояние режимов ученика: %v", err)
	}
	return state
}

func assertStudentModeStateUnchanged(t *testing.T, before, after studentModeState, operation string) {
	t.Helper()
	if before.xmin != after.xmin || !before.updatedAt.Equal(after.updatedAt) ||
		before.chatEnabled != after.chatEnabled || before.lessonEnabled != after.lessonEnabled {
		t.Fatalf("%s создало лишний UPDATE: before=%+v after=%+v", operation, before, after)
	}
}

