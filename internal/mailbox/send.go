package mailbox

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Outgoing is a message to send.
type Outgoing struct {
	From        string
	To, Cc      []string
	Subject     string
	Body        string
	InReplyTo   string
	References  string
	Attachments []SendAttachment
}

// SendAttachment is a file to attach, already read from wherever it is kept.
type SendAttachment struct {
	Name string
	Type string
	Data []byte
}

const maxSendBytes = 20 << 20

func headerSafe(field, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("the %s cannot contain a line break", field)
	}
	return nil
}

// BuildMessage renders an RFC 5322 message: plain text, or multipart/mixed
// with base64 attachments. Header values cannot smuggle extra headers.
func BuildMessage(o Outgoing, now time.Time) ([]byte, error) {
	if len(o.To) == 0 {
		return nil, fmt.Errorf("give at least one recipient")
	}
	for _, f := range []struct{ name, v string }{{"sender", o.From}, {"subject", o.Subject}} {
		if err := headerSafe(f.name, f.v); err != nil {
			return nil, err
		}
	}
	for _, rcpt := range append(append([]string{}, o.To...), o.Cc...) {
		if err := headerSafe("recipient", rcpt); err != nil {
			return nil, err
		}
		if !strings.Contains(rcpt, "@") {
			return nil, fmt.Errorf("%q is not a mail address", rcpt)
		}
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", o.From, strings.Join(o.To, ", "))
	if len(o.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(o.Cc, ", "))
	}
	fmt.Fprintf(&b, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@talaria>\r\n", mime.QEncoding.Encode("UTF-8", o.Subject), now.Format(time.RFC1123Z), hex.EncodeToString(id))
	if o.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", o.InReplyTo)
	}
	if o.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", o.References)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(o.Body, "\r\n", "\n"), "\n", "\r\n")
	if len(o.Attachments) == 0 {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
	} else {
		boundary := "talaria-" + hex.EncodeToString(id)
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, boundary, body)
		for _, a := range o.Attachments {
			typ := a.Type
			if typ == "" {
				typ = "application/octet-stream"
			}
			fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, typ, a.Name, a.Name)
			enc := base64.StdEncoding.EncodeToString(a.Data)
			for len(enc) > 76 {
				b.WriteString(enc[:76] + "\r\n")
				enc = enc[76:]
			}
			b.WriteString(enc + "\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
	}
	if len(b.String()) > maxSendBytes {
		return nil, fmt.Errorf("the message is larger than %d MB", maxSendBytes>>20)
	}
	return []byte(b.String()), nil
}

// SMTPFor derives the submission server from the IMAP one: imap.<domain>
// becomes smtp.<domain> on port 465 (implicit TLS), the usual shape.
func SMTPFor(server string) string {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		host, port = server, "993"
	}
	_ = port
	if strings.HasPrefix(host, "imap.") {
		host = "smtp." + strings.TrimPrefix(host, "imap.")
	}
	return net.JoinHostPort(host, "465")
}

// smtpSend is the real transport: implicit TLS, plain auth, never without TLS.
func smtpSend(_ context.Context, server, user, pass, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return fmt.Errorf("the SMTP server %q is not host:port", server)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", server, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("could not connect to the mail server %s", server)
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("the mail server did not greet us")
	}
	defer c.Close()
	if user != "" {
		if err := c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
			return fmt.Errorf("the mail server refused the sign-in")
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("the mail server refused the sender: %v", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("the mail server refused %s: %v", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("the mail server refused the message")
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("the message could not be written")
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server refused the message")
	}
	return c.Quit()
}

// Send builds, sends and files a copy in Sent (best effort: a filed copy
// failing is reported in the result, not as a failed send).
func (s *Service) Send(ctx context.Context, o Outgoing) (string, error) {
	if s.Address == "" {
		return "", fmt.Errorf("no mailbox is set up")
	}
	if o.From == "" {
		o.From = s.Address
	}
	msg, err := BuildMessage(o, s.now())
	if err != nil {
		return "", err
	}
	send := s.SendMail
	if send == nil {
		send = smtpSend
	}
	server := s.SMTPServer
	if server == "" {
		server = SMTPFor(s.Server)
	}
	rcpts := append(append([]string{}, o.To...), o.Cc...)
	from := o.From
	if addr := addressOnly(from); addr != "" {
		from = addr
	}
	if err := send(ctx, server, s.login(), s.Password, from, rcpts, msg); err != nil {
		return "", s.scrubErr(err)
	}
	out := fmt.Sprintf("Sent %q to %s.\n", o.Subject, strings.Join(o.To, ", "))
	if err := s.Append(ctx, "Sent", msg); err != nil {
		out += "Note: the copy for the Sent folder could not be filed: " + scrubText(s, err) + "\n"
	}
	return out, nil
}

func scrubText(s *Service, err error) string {
	if s.Password == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), s.Password, "<password>")
}

// addressOnly takes the address out of "Name <a@b>".
func addressOnly(from string) string {
	if i, j := strings.Index(from, "<"), strings.Index(from, ">"); i >= 0 && j > i {
		return from[i+1 : j]
	}
	return strings.TrimSpace(from)
}
