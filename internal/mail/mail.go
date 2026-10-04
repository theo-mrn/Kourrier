// Package mail defines the domain types shared by the API, the queue and the
// e-mail providers.
package mail

import (
	netmail "net/mail"
	"strings"
)

// Address is an e-mail address with an optional display name.
type Address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// String formats the address as an RFC 5322 mailbox, encoding the display
// name when it contains non-ASCII characters.
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	return (&netmail.Address{Name: a.Name, Address: a.Email}).String()
}

// Domain returns the lower-cased domain part of the address.
func (a Address) Domain() string {
	_, domain, _ := strings.Cut(a.Email, "@")
	return strings.ToLower(domain)
}

// Request is what a client application asks Kourrier to deliver. Exactly one
// of Template or Content must be set.
type Request struct {
	From     Address           `json:"from"`
	To       []Address         `json:"to"`
	Cc       []Address         `json:"cc,omitempty"`
	Bcc      []Address         `json:"bcc,omitempty"`
	ReplyTo  []Address         `json:"reply_to,omitempty"`
	Template *TemplateRef      `json:"template,omitempty"`
	Content  *Content          `json:"content,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

// TemplateRef points to a template stored in the object storage, along with
// the data used to render it.
type TemplateRef struct {
	Name   string         `json:"name"`
	Locale string         `json:"locale,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// Content is a pre-rendered e-mail body.
type Content struct {
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

// Message is a fully rendered e-mail, ready to be handed to a provider.
type Message struct {
	ID      string
	From    Address
	To      []Address
	Cc      []Address
	Bcc     []Address
	ReplyTo []Address
	Subject string
	HTML    string
	Text    string
	Headers map[string]string
	Tags    map[string]string
}

// Recipients returns every recipient of the message (To, Cc and Bcc).
func (m *Message) Recipients() []Address {
	all := make([]Address, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	all = append(all, m.To...)
	all = append(all, m.Cc...)
	return append(all, m.Bcc...)
}
