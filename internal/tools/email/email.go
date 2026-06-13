// Package email implements a KTP tool for sending and reading email via SMTP and IMAP.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/ktp"
)

// DefaultMaxResults limits the number of emails returned from inbox/search operations.
const DefaultMaxResults = 25

// SecretResolverFunc resolves a secret from the vault with cascading lookup.
type SecretResolverFunc func(ctx context.Context, agentID, teamID, key string) (string, error)

// Config holds static configuration for the email tool.
type Config struct {
	// MaxRecipientsPerSend limits how many recipients per send action (default 10).
	MaxRecipientsPerSend int
	// RateLimitSendsPerHour limits sends per agent per hour (default 30).
	RateLimitSendsPerHour int
}

// Tool implements ktp.Tool for email operations.
type Tool struct {
	secretResolver SecretResolverFunc
	config         Config
}

// New creates a new email tool.
func New(resolver SecretResolverFunc, cfg Config) *Tool {
	if cfg.MaxRecipientsPerSend <= 0 {
		cfg.MaxRecipientsPerSend = 10
	}
	if cfg.RateLimitSendsPerHour <= 0 {
		cfg.RateLimitSendsPerHour = 30
	}
	return &Tool{
		secretResolver: resolver,
		config:         cfg,
	}
}

// Inline returns true — email tool runs in-process (no sandbox needed for credential access).
func (t *Tool) Inline() bool { return true }

// Declaration returns the email tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "email",
		Version:      "1.0.0",
		Description:  "Send and read email via SMTP and IMAP",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{}, // Opt-in only
		Actions: []ktp.ActionSpec{
			{
				Name:        "send",
				Description: "Send an email via SMTP",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"to":      {Type: "string", Description: "Recipient email address(es), comma-separated"},
						"subject": {Type: "string", Description: "Email subject line"},
						"body":    {Type: "string", Description: "Email body (plain text)"},
						"cc":      {Type: "string", Description: "CC recipients, comma-separated"},
						"reply_to": {Type: "string", Description: "Reply-To address"},
					},
					Required: []string{"to", "subject", "body"},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "write", Resource: "*"}},
			},
			{
				Name:        "read_inbox",
				Description: "Read recent emails from IMAP inbox",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"limit":  {Type: "integer", Description: "Max emails to return (default 10)"},
						"folder": {Type: "string", Description: "IMAP folder (default INBOX)"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "read", Resource: "*"}},
			},
			{
				Name:        "search",
				Description: "Search emails by subject, sender, or date range",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"query":  {Type: "string", Description: "Search query (applied to subject and from fields)"},
						"folder": {Type: "string", Description: "IMAP folder to search (default INBOX)"},
						"limit":  {Type: "integer", Description: "Max results (default 10)"},
					},
					Required: []string{"query"},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "network", Access: "read", Resource: "*"}},
			},
		},
	}
}

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()
	switch req.Action {
	case "send":
		return t.send(ctx, req, start)
	case "read_inbox":
		return t.readInbox(ctx, req, start)
	case "search":
		return t.search(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *Tool) send(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	to := strParam(req.Parameters, "to")
	subject := strParam(req.Parameters, "subject")
	body := strParam(req.Parameters, "body")
	cc := strParam(req.Parameters, "cc")
	replyTo := strParam(req.Parameters, "reply_to")

	if to == "" || subject == "" || body == "" {
		return errResp(req.ID, "missing required parameters: to, subject, body"), nil
	}

	// Parse recipients.
	recipients := parseAddresses(to)
	if cc != "" {
		recipients = append(recipients, parseAddresses(cc)...)
	}
	if len(recipients) > t.config.MaxRecipientsPerSend {
		return errResp(req.ID, fmt.Sprintf("too many recipients (%d, max %d)", len(recipients), t.config.MaxRecipientsPerSend)), nil
	}

	// Resolve SMTP credentials from vault.
	smtpCreds, err := t.resolveSMTPCreds(ctx, req.AgentID, req.TeamID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("SMTP credentials not found: %s", err)), nil
	}

	// Build email message.
	msg := buildMessage(smtpCreds.From, recipients, cc, replyTo, subject, body)

	// Send via SMTP.
	if err := sendSMTP(ctx, smtpCreds, recipients, msg); err != nil {
		return errResp(req.ID, fmt.Sprintf("SMTP send failed: %s", err)), nil
	}

	result := map[string]any{
		"status":     "sent",
		"to":         to,
		"subject":    subject,
		"recipients": len(recipients),
	}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

func (t *Tool) readInbox(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	limit := intParam(req.Parameters, "limit", 10)
	folder := strParam(req.Parameters, "folder")
	if folder == "" {
		folder = "INBOX"
	}
	if limit > DefaultMaxResults {
		limit = DefaultMaxResults
	}

	// IMAP is complex to implement without a library. Return a structured stub
	// that documents the IMAP connection requirements.
	imapCreds, err := t.resolveIMAPCreds(ctx, req.AgentID, req.TeamID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("IMAP credentials not found: %s. Set vault keys: email_imap_host, email_imap_user, email_imap_password", err)), nil
	}

	emails, err := fetchIMAPEmails(ctx, imapCreds, folder, "", limit)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("IMAP read failed: %s", err)), nil
	}

	result := map[string]any{
		"folder": folder,
		"count":  len(emails),
		"emails": emails,
	}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

func (t *Tool) search(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	query := strParam(req.Parameters, "query")
	if query == "" {
		return errResp(req.ID, "missing required parameter: query"), nil
	}

	folder := strParam(req.Parameters, "folder")
	if folder == "" {
		folder = "INBOX"
	}
	limit := intParam(req.Parameters, "limit", 10)
	if limit > DefaultMaxResults {
		limit = DefaultMaxResults
	}

	imapCreds, err := t.resolveIMAPCreds(ctx, req.AgentID, req.TeamID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("IMAP credentials not found: %s", err)), nil
	}

	emails, err := fetchIMAPEmails(ctx, imapCreds, folder, query, limit)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("IMAP search failed: %s", err)), nil
	}

	result := map[string]any{
		"folder": folder,
		"query":  query,
		"count":  len(emails),
		"emails": emails,
	}
	resp := ktp.NewToolResponse(req.ID, true, result, "", ms(start))
	return &resp, nil
}

// --- SMTP ---

type smtpCreds struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
}

func (t *Tool) resolveSMTPCreds(ctx context.Context, agentID, teamID string) (*smtpCreds, error) {
	host, err := t.secretResolver(ctx, agentID, teamID, "email_smtp_host")
	if err != nil {
		return nil, fmt.Errorf("email_smtp_host: %w", err)
	}
	user, err := t.secretResolver(ctx, agentID, teamID, "email_smtp_user")
	if err != nil {
		return nil, fmt.Errorf("email_smtp_user: %w", err)
	}
	password, err := t.secretResolver(ctx, agentID, teamID, "email_smtp_password")
	if err != nil {
		return nil, fmt.Errorf("email_smtp_password: %w", err)
	}

	// Port defaults to 587 (STARTTLS).
	port := "587"
	if p, e := t.secretResolver(ctx, agentID, teamID, "email_smtp_port"); e == nil && p != "" {
		port = p
	}

	// From address defaults to the SMTP user.
	from := user
	if f, e := t.secretResolver(ctx, agentID, teamID, "email_from"); e == nil && f != "" {
		from = f
	}

	return &smtpCreds{Host: host, Port: port, User: user, Password: password, From: from}, nil
}

func buildMessage(from string, to []string, cc, replyTo, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	if cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", cc)
	}
	if replyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", replyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(body)
	return b.String()
}

func sendSMTP(_ context.Context, creds *smtpCreds, recipients []string, msg string) error {
	addr := net.JoinHostPort(creds.Host, creds.Port)

	// Connect with TLS.
	tlsConfig := &tls.Config{
		ServerName: creds.Host,
		MinVersion: tls.VersionTLS12,
	}

	var client *smtp.Client
	if creds.Port == "465" {
		// Implicit TLS (SMTPS).
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, tlsConfig)
		if err != nil {
			return fmt.Errorf("TLS dial: %w", err)
		}
		c, err := smtp.NewClient(conn, creds.Host)
		if err != nil {
			conn.Close()
			return fmt.Errorf("SMTP client: %w", err)
		}
		client = c
	} else {
		// STARTTLS (port 587).
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("SMTP dial: %w", err)
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			c.Close()
			return fmt.Errorf("STARTTLS: %w", err)
		}
		client = c
	}
	defer client.Close()

	// Authenticate.
	auth := smtp.PlainAuth("", creds.User, creds.Password, creds.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// Send.
	if err := client.Mail(creds.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := io.WriteString(w, msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return client.Quit()
}

// --- IMAP ---

type imapCreds struct {
	Host     string
	Port     string
	User     string
	Password string
}

type emailSummary struct {
	UID     string `json:"uid"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Snippet string `json:"snippet"`
}

func (t *Tool) resolveIMAPCreds(ctx context.Context, agentID, teamID string) (*imapCreds, error) {
	host, err := t.secretResolver(ctx, agentID, teamID, "email_imap_host")
	if err != nil {
		return nil, fmt.Errorf("email_imap_host: %w", err)
	}
	user, err := t.secretResolver(ctx, agentID, teamID, "email_imap_user")
	if err != nil {
		return nil, fmt.Errorf("email_imap_user: %w", err)
	}
	password, err := t.secretResolver(ctx, agentID, teamID, "email_imap_password")
	if err != nil {
		return nil, fmt.Errorf("email_imap_password: %w", err)
	}
	port := "993"
	if p, e := t.secretResolver(ctx, agentID, teamID, "email_imap_port"); e == nil && p != "" {
		port = p
	}
	return &imapCreds{Host: host, Port: port, User: user, Password: password}, nil
}

// fetchIMAPEmails connects via IMAP, selects the folder, and retrieves email summaries.
// Uses raw IMAP commands (no external library dependency).
func fetchIMAPEmails(_ context.Context, creds *imapCreds, folder, searchQuery string, limit int) ([]emailSummary, error) {
	addr := net.JoinHostPort(creds.Host, creds.Port)

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{
		ServerName: creds.Host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("IMAP TLS dial: %w", err)
	}
	defer conn.Close()

	// Set read deadline.
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	r := newIMAPReader(conn)

	// Read greeting.
	if _, err := r.readLine(); err != nil {
		return nil, fmt.Errorf("read greeting: %w", err)
	}

	// Login.
	if err := r.command(conn, "a001", fmt.Sprintf("LOGIN %s %s", quoteIMAP(creds.User), quoteIMAP(creds.Password))); err != nil {
		return nil, fmt.Errorf("LOGIN: %w", err)
	}

	// Select folder.
	if err := r.command(conn, "a002", fmt.Sprintf("SELECT %s", quoteIMAP(folder))); err != nil {
		return nil, fmt.Errorf("SELECT: %w", err)
	}

	// Search for messages.
	var searchCmd string
	if searchQuery != "" {
		searchCmd = fmt.Sprintf("SEARCH OR SUBJECT %s FROM %s", quoteIMAP(searchQuery), quoteIMAP(searchQuery))
	} else {
		searchCmd = "SEARCH ALL"
	}
	uids, err := r.searchCommand(conn, "a003", searchCmd)
	if err != nil {
		return nil, fmt.Errorf("SEARCH: %w", err)
	}

	// Sort descending (newest first) and limit.
	sort.Sort(sort.Reverse(sort.StringSlice(uids)))
	if len(uids) > limit {
		uids = uids[:limit]
	}

	if len(uids) == 0 {
		_ = r.command(conn, "a099", "LOGOUT")
		return nil, nil
	}

	// Fetch headers for selected messages.
	uidSet := strings.Join(uids, ",")
	emails, err := r.fetchHeaders(conn, "a004", uidSet)
	if err != nil {
		return nil, fmt.Errorf("FETCH: %w", err)
	}

	_ = r.command(conn, "a099", "LOGOUT")
	return emails, nil
}

// --- IMAP reader (minimal raw protocol) ---

type imapReader struct {
	buf  []byte
	conn io.Reader
}

func newIMAPReader(conn io.Reader) *imapReader {
	return &imapReader{buf: make([]byte, 0, 4096), conn: conn}
}

func (r *imapReader) readLine() (string, error) {
	tmp := make([]byte, 1)
	var line []byte
	for {
		n, err := r.conn.Read(tmp)
		if err != nil {
			return string(line), err
		}
		if n > 0 {
			line = append(line, tmp[0])
			if tmp[0] == '\n' {
				return strings.TrimRight(string(line), "\r\n"), nil
			}
		}
	}
}

func (r *imapReader) readUntilTag(tag string) ([]string, error) {
	var lines []string
	for {
		line, err := r.readLine()
		if err != nil {
			return lines, err
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, tag+" ") {
			if strings.Contains(line, "NO") || strings.Contains(line, "BAD") {
				return lines, fmt.Errorf("IMAP error: %s", line)
			}
			return lines, nil
		}
	}
}

func (r *imapReader) command(conn io.Writer, tag, cmd string) error {
	_, err := fmt.Fprintf(conn, "%s %s\r\n", tag, cmd)
	if err != nil {
		return err
	}
	_, err = r.readUntilTag(tag)
	return err
}

func (r *imapReader) searchCommand(conn io.Writer, tag, cmd string) ([]string, error) {
	_, err := fmt.Fprintf(conn, "%s %s\r\n", tag, cmd)
	if err != nil {
		return nil, err
	}
	lines, err := r.readUntilTag(tag)
	if err != nil {
		return nil, err
	}

	// Parse SEARCH response.
	var uids []string
	for _, line := range lines {
		if strings.HasPrefix(line, "* SEARCH") {
			parts := strings.Fields(line)
			if len(parts) > 2 {
				uids = append(uids, parts[2:]...)
			}
		}
	}
	return uids, nil
}

func (r *imapReader) fetchHeaders(conn io.Writer, tag, uidSet string) ([]emailSummary, error) {
	cmd := fmt.Sprintf("FETCH %s (BODY.PEEK[HEADER.FIELDS (FROM SUBJECT DATE)])", uidSet)
	_, err := fmt.Fprintf(conn, "%s %s\r\n", tag, cmd)
	if err != nil {
		return nil, err
	}

	lines, err := r.readUntilTag(tag)
	if err != nil {
		return nil, err
	}

	// Parse FETCH responses (simplified).
	var emails []emailSummary
	var current *emailSummary
	for _, line := range lines {
		if strings.Contains(line, "FETCH") && strings.Contains(line, "BODY") {
			if current != nil {
				emails = append(emails, *current)
			}
			current = &emailSummary{}
			// Extract UID from "* N FETCH".
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				current.UID = parts[1]
			}
		} else if current != nil {
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "from:") {
				current.From = strings.TrimSpace(line[5:])
			} else if strings.HasPrefix(lower, "subject:") {
				current.Subject = strings.TrimSpace(line[8:])
			} else if strings.HasPrefix(lower, "date:") {
				current.Date = strings.TrimSpace(line[5:])
			}
		}
	}
	if current != nil && current.UID != "" {
		emails = append(emails, *current)
	}

	return emails, nil
}

func quoteIMAP(s string) string {
	// Simple IMAP quoting — wraps in double quotes, escapes backslashes and quotes.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// --- helpers ---

func parseAddresses(s string) []string {
	var addrs []string
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			addrs = append(addrs, a)
		}
	}
	return addrs
}

func strParam(params map[string]any, key string) string {
	raw, ok := params[key]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}

func intParam(params map[string]any, key string, defaultVal int) int {
	raw, ok := params[key]
	if !ok {
		return defaultVal
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return defaultVal
	}
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
