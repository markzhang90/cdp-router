package biz

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	ValidationUnknown     = "UNKNOWN"
	ValidationPass        = "PASS"
	ValidationSkipped     = "SKIPPED"
	ValidationRisk        = "RISK"
	ValidationInvalid     = "INVALID"
	RiskAnswerTooShort    = "ANSWER_TOO_SHORT"
	RiskAnswerKeyword     = "ANSWER_KEYWORD_MATCH"
	RiskAnswerNoReference = "ANSWER_NO_REFERENCE"
)

var ErrValidationRulesConflict = errors.New("validation rules changed; reload before saving")

type AnswerRisk struct {
	Code            string   `json:"code"`
	Message         string   `json:"message"`
	ActualLength    int      `json:"actualLength,omitempty"`
	MinLength       int      `json:"minLength,omitempty"`
	MatchedKeywords []string `json:"matchedKeywords,omitempty"`
}

type AnswerValidation struct {
	Status      string       `json:"validationStatus"`
	RiskItems   []AnswerRisk `json:"riskItems"`
	RuleVersion string       `json:"validationRuleVersion"`
	ValidatedAt time.Time    `json:"validatedAt"`
}

type ResultValidationRules struct {
	Version                          string
	Keywords                         []string
	RiskCallbackAllowedProviderCodes []string
	UpdatedAt                        time.Time
	UpdatedBy                        string
}

func normalizedKeyword(value string) string {
	return strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
}

func NormalizeValidationKeywords(keywords []string) ([]string, error) {
	if len(keywords) > 2000 {
		return nil, fmt.Errorf("%w: at most 2000 keywords", ErrInvalidTaskRequest)
	}
	out := make([]string, 0, len(keywords))
	seen := make(map[string]bool, len(keywords))
	for _, keyword := range keywords {
		keyword = normalizedKeyword(keyword)
		if keyword == "" || len([]rune(keyword)) > 200 {
			return nil, fmt.Errorf("%w: keywords must contain 1–200 characters", ErrInvalidTaskRequest)
		}
		if seen[keyword] {
			return nil, fmt.Errorf("%w: duplicate keyword", ErrInvalidTaskRequest)
		}
		seen[keyword] = true
		out = append(out, keyword)
	}
	return out, nil
}

// ValidateAnswer checks one already-parsed answer. Quality findings never alter
// source text or parse status. The immutable rules snapshot is reused per import.
func ValidateAnswer(answer TaskItemResult, rules ResultValidationRules) AnswerValidation {
	result := AnswerValidation{Status: ValidationPass, RuleVersion: rules.Version, ValidatedAt: time.Now().UTC(), RiskItems: []AnswerRisk{}}
	length := 0
	for _, r := range answer.AnswerText {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			length++
		}
	}
	if length < 20 {
		result.RiskItems = append(result.RiskItems, AnswerRisk{Code: RiskAnswerTooShort, Message: "回答少于20字", ActualLength: length, MinLength: 20})
		result.Status = ValidationInvalid
	}
	text := normalizedKeyword(answer.AnswerText)
	matched := []string{}
	for _, keyword := range rules.Keywords {
		if keyword != "" && strings.Contains(text, keyword) {
			matched = append(matched, keyword)
		}
	}
	if len(matched) > 0 {
		result.RiskItems = append(result.RiskItems, AnswerRisk{Code: RiskAnswerKeyword, Message: "回答命中关键词", MatchedKeywords: matched})
		result.Status = ValidationInvalid
	}
	if answer.ReferenceCount <= 0 {
		result.RiskItems = append(result.RiskItems, AnswerRisk{Code: RiskAnswerNoReference, Message: "回答溯源引用数未大于0"})
		if result.Status == ValidationPass {
			result.Status = ValidationRisk
		}
	}
	return result
}
