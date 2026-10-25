package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ChoiceData is what a choice button carries back: "ch:<panelID>:<option>".
// The gateway turns a tap into words naming the panel and the option number;
// the agent records the pick with choice_select, which knows the panel.
func ChoiceData(panelID string, idx int) string {
	return "ch:" + panelID + ":" + strconv.Itoa(idx)
}

// ParseChoice reads what ChoiceData built.
func ParseChoice(data string) (panelID string, idx int, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "ch" || parts[1] == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return parts[1], n, true
}

var choiceHead = regexp.MustCompile(`(?m)^question ([0-9a-f]{32}):\n.+ \((?:pick one|compare), \d+ options[^\n]*\n`)
var choiceOption = regexp.MustCompile(`(?m)^(\d+)\.\s+(.+)$`)
var choiceDetail = regexp.MustCompile(`(?m)^\s+-\s+(.+)$`)

// ParseChoicePanel reads a presented question back out of its text form: the
// panel id and the option labels in order. Anything else is not a panel.
func ParseChoicePanel(text string) (string, []string, bool) {
	head := choiceHead.FindStringSubmatch(text)
	if head == nil {
		return "", nil, false
	}
	rest := text[len(head[0]):]
	var labels []string
	for _, line := range strings.Split(rest, "\n") {
		if m := choiceOption.FindStringSubmatch(line); m != nil {
			labels = append(labels, strings.TrimSuffix(strings.TrimSpace(m[2]), " ★"))
			continue
		}
		if choiceDetail.MatchString(line) {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		break
	}
	if len(labels) == 0 || len(labels) > 12 {
		return "", nil, false
	}
	return head[1], labels, true
}

// PickText is what a tap becomes in the conversation: unambiguous for the
// agent, readable for the user.
func PickText(panelID string, idx int) string {
	return fmt.Sprintf("I pick option %d for question %s", idx+1, panelID)
}
