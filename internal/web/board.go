package web

import (
	"errors"
	"net/http"
	"strings"

	"talaria/internal/kanban"
)

// BoardHooks connect the board to what runs its cards.
type BoardHooks struct {
	// Kick starts the Ready cards that can be started now. Nil leaves that to the scheduler's next look.
	Kick func()
	// Stop asks for a running card's task to be cancelled, and says whether it was.
	Stop func(job string) bool
}

func (s *Server) boardOn() bool { return s.Cards != nil }

// savedAgentNames are the saved agents' names, for choosing who does a card.
func (s *Server) savedAgentNames() []string {
	names := []string{}
	if s.Agents != nil {
		for _, a := range s.Agents.List() {
			names = append(names, a.Name)
		}
	}
	return names
}

func (s *Server) boardState() (map[string]any, error) {
	cards, err := s.Cards.List("web", ChatID)
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []kanban.Card{}
	}
	for i := range cards {
		cards[i].Gateway, cards[i].ChatID = "", 0
		if s.Scrub != nil {
			cards[i].Result = s.Scrub(cards[i].Result)
		}
	}
	return map[string]any{"available": true, "columns": kanban.Columns, "cards": cards, "agents": s.savedAgentNames(),
		"main": kanban.Main, "max_cards": kanban.MaxCards}, nil
}

// boardGet is the board: every card in its column, and the agents a card can be given.
func (s *Server) boardGet(w http.ResponseWriter, r *http.Request) {
	if !s.boardOn() {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "columns": kanban.Columns, "cards": []kanban.Card{}, "agents": []string{}})
		return
	}
	st, err := s.boardState()
	if err != nil {
		fail(w, http.StatusInternalServerError, "the board could not be read")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) boardKick() {
	if s.Sources.Board != nil && s.Sources.Board.Kick != nil {
		s.Sources.Board.Kick()
	}
}

// boardPost changes the board: add, edit, move and remove cards, and stop a running one. Moving a card to Ready is
// what starts it, and it is the person's own action here, so it needs no further question; a card can be given the
// right to use risky tools only here, by them, never by the agent without asking.
func (s *Server) boardPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action     string  `json:"action"`
		ID         string  `json:"id"`
		Title      *string `json:"title"`
		Body       *string `json:"body"`
		Column     string  `json:"column"`
		Agent      *string `json:"agent"`
		AllowRisky *bool   `json:"allow_risky"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !s.boardOn() {
		fail(w, http.StatusNotImplemented, "the board is not set up here")
		return
	}
	saved := s.savedAgentNames()
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	if in.Agent != nil {
		if err := kanban.ValidAgent(*in.Agent, saved); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	agent := func() string { return kanban.CanonicalAgent(str(in.Agent), saved) }
	var err error
	var card kanban.Card
	switch in.Action {
	case "add":
		col := strings.ToLower(strings.TrimSpace(in.Column))
		if col != "" && col != kanban.Backlog && col != kanban.Ready {
			fail(w, http.StatusBadRequest, "a new card goes to backlog or ready")
			return
		}
		card, err = s.Cards.Add("web", ChatID, kanban.Card{Title: str(in.Title), Body: str(in.Body), Column: col, Agent: agent(), AllowRisky: in.AllowRisky != nil && *in.AllowRisky})
		if err == nil && card.Column == kanban.Ready {
			s.boardKick()
		}
	case "edit":
		card, err = s.Cards.Edit("web", ChatID, in.ID, func(c *kanban.Card) {
			if in.Title != nil {
				c.Title = *in.Title
			}
			if in.Body != nil {
				c.Body = *in.Body
			}
			if in.Agent != nil {
				c.Agent = agent()
			}
			if in.AllowRisky != nil {
				c.AllowRisky = *in.AllowRisky
			}
		})
	case "move":
		if strings.EqualFold(strings.TrimSpace(in.Column), kanban.Ready) && (in.Agent != nil || in.AllowRisky != nil) {
			if _, err = s.Cards.Edit("web", ChatID, in.ID, func(c *kanban.Card) {
				if in.Agent != nil {
					c.Agent = agent()
				}
				if in.AllowRisky != nil {
					c.AllowRisky = *in.AllowRisky
				}
			}); err != nil {
				break
			}
		}
		card, err = s.Cards.Move("web", ChatID, in.ID, in.Column)
		if err == nil && card.Column == kanban.Ready {
			s.boardKick()
		}
	case "stop":
		c, gerr := s.Cards.Get("web", ChatID, in.ID)
		switch {
		case gerr != nil:
			err = gerr
		case c.Column != kanban.Running:
			fail(w, http.StatusBadRequest, "that is not running")
			return
		case s.Sources.Board == nil || s.Sources.Board.Stop == nil || c.Job == "" || !s.Sources.Board.Stop(c.Job):
			fail(w, http.StatusBadRequest, "its task could not be stopped just now: it may have just finished, or has not started yet")
			return
		}
	case "remove":
		err = s.Cards.Delete("web", ChatID, in.ID)
	default:
		fail(w, http.StatusBadRequest, "that is not something the board does")
		return
	}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, kanban.ErrNoCard) {
			code = http.StatusNotFound
		}
		fail(w, code, err.Error())
		return
	}
	st, serr := s.boardState()
	if serr != nil {
		fail(w, http.StatusInternalServerError, "the board could not be read")
		return
	}
	st["card"] = card.ID
	writeJSON(w, http.StatusOK, st)
}
