package app

import (
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/execenv"
	"talaria/internal/kanban"
)

func TestBoardToolsAreOfferedWhereTheyBelong(t *testing.T) {
	cards := &kanban.Store{Path: filepath.Join(t.TempDir(), "sessions.db")}
	rt := openWith(t, Config{Env: execenv.Local{}, Cards: cards})
	chat := AgentOptions{ChatID: 5, Gateway: "discord", Tasks: noControl{}}

	// A chat can read, add, move and remove cards.
	tools := toolSet(rt, chat)
	for _, want := range []string{"kanban_list", "kanban_add", "kanban_move", "kanban_remove"} {
		if !tools[want] {
			t.Errorf("a chat should have %s", want)
		}
	}

	// Unattended work, which a card's own task is, can read the board and start nothing from it.
	unattended := chat
	unattended.NoCron = true
	tools = toolSet(rt, unattended)
	if !tools["kanban_list"] {
		t.Error("work started from a card should be able to read the board")
	}
	for _, never := range []string{"kanban_add", "kanban_move", "kanban_remove"} {
		if tools[never] {
			t.Errorf("unattended work must not have %s: a card's task could start more cards", never)
		}
	}

	// With nothing to run tasks on (the command line), cards can be read and never started.
	noTasks := chat
	noTasks.Tasks = nil
	tools = toolSet(rt, noTasks)
	if !tools["kanban_list"] || tools["kanban_add"] || tools["kanban_move"] {
		t.Errorf("without a scheduler only kanban_list should be offered: %v", tools)
	}

	// No chat, no board.
	for name := range toolSet(rt, AgentOptions{}) {
		if strings.HasPrefix(name, "kanban_") {
			t.Errorf("%s without a chat", name)
		}
	}
	// No store, no tools.
	off := openWith(t, Config{Env: execenv.Local{}})
	for name := range toolSet(off, chat) {
		if strings.HasPrefix(name, "kanban_") {
			t.Errorf("%s with the board off", name)
		}
	}
}
