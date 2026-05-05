package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
)

// ----- messages -----

type (
	wsMsg      string
	connectMsg struct {
		conn *websocket.Conn
		err  error
	}
	retryMsg struct{}
)

// ----- commands -----

func connectCmd(rawURL string) tea.Cmd {
	return func() tea.Msg {
		u, err := url.Parse(rawURL)
		if err != nil {
			return connectMsg{err: fmt.Errorf("bad url: %w", err)}
		}
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		return connectMsg{conn: conn, err: err}
	}
}

func readCmd(conn *websocket.Conn) tea.Cmd {
	return func() tea.Msg {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return connectMsg{err: fmt.Errorf("read: %w", err)}
		}
		return wsMsg(string(msg))
	}
}

func writeCmd(conn *websocket.Conn, text string) tea.Cmd {
	return func() tea.Msg {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
			return connectMsg{err: fmt.Errorf("write: %w", err)}
		}
		return nil
	}
}

// ----- colors -----

const (
	colorCyan   = lipgloss.Color("6")
	colorYellow = lipgloss.Color("3")
	colorGreen  = lipgloss.Color("2")
	colorRed    = lipgloss.Color("1")
	colorGray   = lipgloss.Color("8")
	colorWhite  = lipgloss.Color("7")
)

// ----- model -----

type model struct {
	conn     *websocket.Conn
	url      string
	viewport viewport.Model
	textarea textarea.Model
	msgs     []string
	width    int
	height   int
	ready    bool
}

func (m *model) appendMsg(prefix string, c lipgloss.Color, msg string) {
	ts := time.Now().Format("15:04:05")

	tsStyle := lipgloss.NewStyle().Foreground(colorGray)
	prefixStyle := lipgloss.NewStyle().Foreground(c).Bold(true)

	line := tsStyle.Render(ts+" ") + prefixStyle.Render(prefix) + " " + msg
	m.msgs = append(m.msgs, line)
	m.viewport.SetContent(strings.Join(m.msgs, "\n"))
	m.viewport.GotoBottom()
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		connectCmd(m.url),
		m.textarea.Focus(),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+c", "esc"))):
			if m.conn != nil {
				m.conn.Close()
			}
			return m, tea.Quit

		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if !m.ready || m.conn == nil {
				return m, nil
			}
			text := strings.TrimSpace(m.textarea.Value())
			if text == "" {
				return m, nil
			}
			cmds = append(cmds, writeCmd(m.conn, text))
			m.appendMsg(">", colorYellow, text)
			m.textarea.Reset()
			return m, tea.Batch(cmds...)
		}

	case connectMsg:
		if msg.err != nil {
			m.ready = false
			if m.conn != nil {
				m.conn.Close()
			}
			m.conn = nil
			m.appendMsg("x", colorRed, msg.err.Error())
			return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
				return retryMsg{}
			})
		}
		m.conn = msg.conn
		m.ready = true
		m.appendMsg("+", colorGreen, "Connected to "+m.url)
		return m, readCmd(m.conn)

	case retryMsg:
		m.appendMsg("~", colorGray, "Retrying "+m.url+" ...")
		return m, connectCmd(m.url)

	case wsMsg:
		m.appendMsg("<", colorCyan, string(msg))
		return m, readCmd(m.conn)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerHeight := 1
		inputBorderHeight := 2
		inputContentHeight := m.textarea.Height()
		helpHeight := 1
		separatorHeight := 1
		viewportBorderHeight := 2

		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - headerHeight - viewportBorderHeight -
			separatorHeight - inputBorderHeight - inputContentHeight - helpHeight
		if m.viewport.Height < 1 {
			m.viewport.Height = 1
		}

		m.textarea.SetWidth(msg.Width - 8)
	}

	// Let the sub-components handle the rest.
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.textarea, cmd = m.textarea.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.width == 0 {
		return lipgloss.NewStyle().
			Foreground(colorYellow).
			Margin(1, 2).
			Render(fmt.Sprintf(
				"Connecting to %s ...\n\n"+
					"If this persists, check:\n"+
					"  - Is the gateway running?\n"+
					"  - Is the addr correct? (use -addr flag)\n"+
					"  - Is the port reachable?",
				m.url,
			))
	}

	header := lipgloss.NewStyle().
		Foreground(colorWhite).
		Bold(true).
		Padding(0, 1).
		Render("WebSocket Client  |  " + m.url)

	separator := lipgloss.NewStyle().
		Foreground(colorGray).
		Render(strings.Repeat("-", m.width))

	viewportBox := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorCyan).
		Padding(0, 1).
		Width(m.width - 2).
		Render(m.viewport.View())

	inputBox := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorYellow).
		Padding(0, 1).
		Width(m.width - 2).
		Render(m.textarea.View())

	statusIndicator := "●"
	statusColor := colorRed
	if m.ready {
		statusColor = colorGreen
	}
	status := lipgloss.NewStyle().Foreground(statusColor).Render(statusIndicator)

	help := lipgloss.NewStyle().
		Foreground(colorGray).
		Padding(0, 1).
		Render(status + "  Enter: send  |  Ctrl+C / Esc: quit  |  Up/Down: scroll")

	return lipgloss.JoinVertical(
		lipgloss.Top,
		header,
		separator,
		viewportBox,
		inputBox,
		help,
	)
}

func buildURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "ws://" + addr
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7272", "WebSocket server addr (ip:port or full URL)")
	debug := flag.Bool("debug", false, "Write debug logs to ws-client-debug.log")
	flag.Parse()

	wsURL := buildURL(*addr)

	ta := textarea.New()
	ta.Placeholder = "Type a message and press Enter..."
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = "> "
	// Disable Enter from inserting newlines — we use it to send messages.
	ta.KeyMap.InsertNewline.SetEnabled(false)

	m := &model{
		url:      wsURL,
		viewport: viewport.New(80, 20),
		textarea: ta,
	}

	m.viewport.Style = lipgloss.NewStyle()

	if *debug {
		f, err := tea.LogToFile("ws-client-debug.log", "ws-client")
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot create debug log: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
