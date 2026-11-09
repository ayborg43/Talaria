package watch

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Condition kinds. A watch with a condition is settled without the model: the
// poll's answer either meets it or does not, and the user hears once, when it
// first does.
const (
	PriceBelow  = "price_below"
	PriceAbove  = "price_above"
	TextAppears = "text_appears"
	TextGone    = "text_gone"
)

// Condition is what a watch waits for, when it waits for one thing rather than
// for any change.
type Condition struct {
	Kind string `json:"kind"`
	// USD is the threshold of a price condition, in dollars.
	USD float64 `json:"usd,omitempty"`
	// Text is what a text condition looks for, ignoring case.
	Text string `json:"text,omitempty"`
	// Near narrows a price condition to the first price after this text, for a
	// page that shows many. Empty takes the first price on the page.
	Near string `json:"near,omitempty"`
}

func (c *Condition) validate() error {
	c.Text, c.Near = strings.TrimSpace(c.Text), strings.TrimSpace(c.Near)
	switch c.Kind {
	case PriceBelow, PriceAbove:
		if c.USD <= 0 || c.USD > 1e9 {
			return errors.New("a price condition needs usd, the threshold in dollars")
		}
		c.Text = ""
	case TextAppears, TextGone:
		if c.Text == "" {
			return errors.New("a text condition needs text, the words to look for")
		}
		c.USD, c.Near = 0, ""
	default:
		return fmt.Errorf("a condition's kind is one of %s, %s, %s or %s", PriceBelow, PriceAbove, TextAppears, TextGone)
	}
	if len([]rune(c.Text)) > 200 || len([]rune(c.Near)) > 200 {
		return errors.New("the condition's text is too long (200 characters at most)")
	}
	return nil
}

// Describe says the condition in words.
func (c Condition) Describe() string {
	near := ""
	if c.Near != "" {
		near = fmt.Sprintf(" (the price after %q)", c.Near)
	}
	switch c.Kind {
	case PriceBelow:
		return fmt.Sprintf("the price is at or below $%s%s", money(c.USD), near)
	case PriceAbove:
		return fmt.Sprintf("the price is at or above $%s%s", money(c.USD), near)
	case TextAppears:
		return fmt.Sprintf("the page says %q", c.Text)
	case TextGone:
		return fmt.Sprintf("the page no longer says %q", c.Text)
	}
	return c.Kind
}

func money(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	return strings.TrimSuffix(s, ".00")
}

// usdPrice finds dollar amounts written the usual ways: $1,299.00, US$ 49,
// USD 12.50, 12.50 USD.
var usdPrice = regexp.MustCompile(`(?i)(?:US\$|\$|USD)\s?(\d{1,3}(?:,\d{3})+|\d+)(\.\d{1,2})?|(\d{1,3}(?:,\d{3})+|\d+)(\.\d{1,2})?\s?USD\b`)

// FindUSD returns the first dollar price in text, or the first one after near
// when that is given and found.
func FindUSD(text, near string) (float64, bool) {
	if near != "" {
		i := strings.Index(strings.ToLower(text), strings.ToLower(near))
		if i < 0 {
			return 0, false
		}
		text = text[i+len(near):]
	}
	m := usdPrice.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	whole, frac := m[1], m[2]
	if whole == "" {
		whole, frac = m[3], m[4]
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(whole, ",", "")+frac, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Check says whether result meets the condition, and what it found: the price
// read, or a word on the text. known is false when a price condition found no
// price to judge, which is neither met nor unmet.
func (c Condition) Check(result string) (met bool, found string, known bool) {
	switch c.Kind {
	case PriceBelow, PriceAbove:
		v, ok := FindUSD(result, c.Near)
		if !ok {
			return false, "", false
		}
		found = "$" + money(v)
		if c.Kind == PriceBelow {
			return v <= c.USD, found, true
		}
		return v >= c.USD, found, true
	case TextAppears, TextGone:
		has := strings.Contains(strings.ToLower(result), strings.ToLower(c.Text))
		return has == (c.Kind == TextAppears), "", true
	}
	return false, "", false
}

// ConditionMessage is what the user is told when a watch's condition is first
// met. It is written here, not by the model: nothing from the page is in it but
// the price that was read.
func ConditionMessage(w Watch, found string) string {
	msg := fmt.Sprintf("%s: %s", w.Title, w.When.Describe())
	if found != "" {
		msg += " — it is now " + found
	}
	return msg + ".\n\nI will tell you again only if it stops being true and then becomes true once more."
}
