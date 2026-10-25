package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Quantum103/Telegramm-Bot/backend/internal/domain"
	"github.com/Quantum103/Telegramm-Bot/backend/internal/service"
	"github.com/jackc/pgx/v5/pgxpool"
)

func questionUpdateSecurityFixture(t *testing.T) (context.Context, *pgxpool.Pool, *service.AuthoringService, int64, int64, int64) {
	t.Helper()
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	suffix := fmt.Sprintf("question-update-security-%d", time.Now().UnixNano())
	var staffID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO staff_accounts (display_name, email, account_status)
		VALUES ('Question security test', $1, 'active')
		RETURNING id
	`, suffix+"@example.test").Scan(&staffID); err != nil {
		t.Fatalf("create staff account: %v", err)
	}

	authoring := service.NewAuthoringService(NewCatalogRepository(pool))
	course, err := authoring.CreateDraftCourse(ctx, domain.CreateCourseParams{
		Slug: suffix, Title: "Question update security test", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create draft course: %v", err)
	}
	lesson, err := authoring.CreateDraftLesson(ctx, domain.CreateLessonParams{
		CourseID: course.ID, Title: "Question update security lesson", CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create draft lesson: %v", err)
	}
	question, err := authoring.CreateQuestion(ctx, domain.CreateQuestionParams{
		LessonVersionID:  lesson.LessonVersionID,
		QuestionNumber:   1,
		Prompt:           "Original prompt",
		ExpectedAnswer:   "Original answer",
		AnswerType:       "text",
		CreatedByStaffID: staffID,
	})
	if err != nil {
		t.Fatalf("create original question: %v", err)
	}
	return ctx, pool, authoring, staffID, lesson.LessonVersionID, question.ID
}

func TestUpdateDraftQuestionRejectsInvalidDataWithoutMutation(t *testing.T) {
	ctx, pool, authoring, staffID, versionID, questionID := questionUpdateSecurityFixture(t)

	// Compare against the fixture's baseline so pre-existing audit rows cannot
	// make this test flaky when the integration database is reused.
	var baselineUpdateAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE entity_type = 'question' AND entity_id = $1 AND action = 'question.draft.update'
	`, questionID).Scan(&baselineUpdateAuditCount); err != nil {
		t.Fatalf("count baseline question update audit events: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*domain.CreateQuestionParams)
	}{
		{
			name: "unsupported answer type",
			mutate: func(params *domain.CreateQuestionParams) {
				params.AnswerType = "binary"
			},
		},
		{
			name: "duplicate option keys",
			mutate: func(params *domain.CreateQuestionParams) {
				params.AnswerType = "single_choice"
				params.Options = []domain.QuestionOptionInput{
					{Key: "A", Text: "First", IsCorrect: true},
					{Key: "A", Text: "Duplicate"},
				}
			},
		},
		{
			name: "single choice without exactly one correct option",
			mutate: func(params *domain.CreateQuestionParams) {
				params.AnswerType = "single_choice"
				params.Options = []domain.QuestionOptionInput{
					{Key: "A", Text: "First"},
					{Key: "B", Text: "Second"},
				}
			},
		},
		{
			name: "oversized option text",
			mutate: func(params *domain.CreateQuestionParams) {
				params.AnswerType = "single_choice"
				params.Options = []domain.QuestionOptionInput{
					{Key: "A", Text: strings.Repeat("x", 1001), IsCorrect: true},
					{Key: "B", Text: "Second"},
				}
			},
		},
		{
			name: "invalid Bible passage range",
			mutate: func(params *domain.CreateQuestionParams) {
				params.BiblePassages = []domain.BiblePassageInput{{
					BookName: "John", Chapter: 3, StartVerse: 17, EndVerse: 16,
				}}
			},
		},
		{
			name: "oversized explanation",
			mutate: func(params *domain.CreateQuestionParams) {
				value := strings.Repeat("x", 4001)
				params.ExpectedExplanation = &value
			},
		},
		{
			name: "oversized prompt",
			mutate: func(params *domain.CreateQuestionParams) {
				params.Prompt = strings.Repeat("x", 4001)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := domain.CreateQuestionParams{
				LessonVersionID: versionID,
				QuestionNumber:  2,
				Prompt:          "Updated prompt",
				ExpectedAnswer:  "Updated answer",
				AnswerType:      "text",
			}
			tt.mutate(&params)
			if _, err := authoring.UpdateQuestion(ctx, questionID, params, staffID); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("update error = %v, want ErrInvalidInput", err)
			}
		})
	}

	var prompt, answer, answerType string
	var updateAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT prompt, expected_answer, answer_type FROM lesson_questions WHERE id = $1
	`, questionID).Scan(&prompt, &answer, &answerType); err != nil {
		t.Fatalf("read question after invalid updates: %v", err)
	}
	if prompt != "Original prompt" || answer != "Original answer" || answerType != "text" {
		t.Fatalf("invalid update mutated question: prompt=%q answer=%q type=%q", prompt, answer, answerType)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE entity_type = 'question' AND entity_id = $1 AND action = 'question.draft.update'
	`, questionID).Scan(&updateAuditCount); err != nil {
		t.Fatalf("count update audit events: %v", err)
	}
	if updateAuditCount != baselineUpdateAuditCount {
		t.Fatalf("invalid updates changed update audit count from %d to %d; want no new audit events",
			baselineUpdateAuditCount, updateAuditCount)
	}
}

func TestUpdateDraftQuestionRollsBackWhenAuditInsertFails(t *testing.T) {
	ctx, pool, authoring, staffID, versionID, questionID := questionUpdateSecurityFixture(t)

	var baselineUpdateAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE entity_type = 'question' AND entity_id = $1 AND action = 'question.draft.update'
	`, questionID).Scan(&baselineUpdateAuditCount); err != nil {
		t.Fatalf("count baseline question update audit events: %v", err)
	}

	functionName := fmt.Sprintf("reject_question_update_audit_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON audit_events", triggerName)); err != nil {
			t.Errorf("drop audit failure trigger: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName)); err != nil {
			t.Errorf("drop audit failure function: %v", err)
		}
	})

	functionSQL := fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.action = 'question.draft.update' THEN
				RAISE EXCEPTION 'forced audit insert failure for atomicity test';
			END IF;
			RETURN NEW;
		END;
		$$
	`, functionName)
	if _, err := pool.Exec(ctx, functionSQL); err != nil {
		t.Fatalf("create audit failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION %s()",
		triggerName, functionName,
	)); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}

	_, err := authoring.UpdateQuestion(ctx, questionID, domain.CreateQuestionParams{
		LessonVersionID: versionID,
		QuestionNumber:  2,
		Prompt:          "Must roll back",
		ExpectedAnswer:  "Must roll back",
		AnswerType:      "single_choice",
		Options: []domain.QuestionOptionInput{
			{Key: "A", Text: "Correct", IsCorrect: true},
			{Key: "B", Text: "Incorrect"},
		},
	}, staffID)
	if err == nil {
		t.Fatal("update succeeded despite forced audit insert failure")
	}

	var prompt, answer, answerType string
	var optionCount, updateAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT q.prompt, q.expected_answer, q.answer_type,
		       (SELECT count(*) FROM question_options o WHERE o.question_id = q.id)
		FROM lesson_questions q WHERE q.id = $1
	`, questionID).Scan(&prompt, &answer, &answerType, &optionCount); err != nil {
		t.Fatalf("read question after failed transaction: %v", err)
	}
	if prompt != "Original prompt" || answer != "Original answer" || answerType != "text" || optionCount != 0 {
		t.Fatalf("failed update was not rolled back: prompt=%q answer=%q type=%q options=%d",
			prompt, answer, answerType, optionCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE entity_type = 'question' AND entity_id = $1 AND action = 'question.draft.update'
	`, questionID).Scan(&updateAuditCount); err != nil {
		t.Fatalf("count update audit events after failure: %v", err)
	}
	if updateAuditCount != baselineUpdateAuditCount {
		t.Fatalf("failed update changed update audit count from %d to %d; want transaction rollback",
			baselineUpdateAuditCount, updateAuditCount)
	}
}
