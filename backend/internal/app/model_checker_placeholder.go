package app

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// replacePlaceholders replaces placeholders in the question with dynamic values
func replacePlaceholders(question string) string {
	now := time.Now()

	// {{random}} - Random number 0-999999
	question = strings.ReplaceAll(question, "{{random}}", fmt.Sprintf("%d", rand.Intn(1000000)))

	// {{date}} - Current date in YYYYMMDD format
	question = strings.ReplaceAll(question, "{{date}}", now.Format("20060102"))

	// {{timestamp}} - Unix timestamp
	question = strings.ReplaceAll(question, "{{timestamp}}", fmt.Sprintf("%d", now.Unix()))

	// {{time}} - Current time in HH:MM:SS format
	question = strings.ReplaceAll(question, "{{time}}", now.Format("15:04:05"))

	// {{uuid}} - UUID v4
	question = strings.ReplaceAll(question, "{{uuid}}", uuid.New().String())

	// {{random:1-100}} - Random number in range
	rangeRegex := regexp.MustCompile(`\{\{random:(\d+)-(\d+)\}\}`)
	question = rangeRegex.ReplaceAllStringFunc(question, func(match string) string {
		matches := rangeRegex.FindStringSubmatch(match)
		if len(matches) == 3 {
			min, _ := strconv.Atoi(matches[1])
			max, _ := strconv.Atoi(matches[2])
			if min < max {
				return fmt.Sprintf("%d", min+rand.Intn(max-min+1))
			}
		}
		return match
	})

	// {{choice:A|B|C}} - Random choice from options
	choiceRegex := regexp.MustCompile(`\{\{choice:([^}]+)\}\}`)
	question = choiceRegex.ReplaceAllStringFunc(question, func(match string) string {
		matches := choiceRegex.FindStringSubmatch(match)
		if len(matches) == 2 {
			options := strings.Split(matches[1], "|")
			if len(options) > 0 {
				return options[rand.Intn(len(options))]
			}
		}
		return match
	})

	return question
}
