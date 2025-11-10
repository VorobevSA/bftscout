package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func padToWidth(s string, width int) string {
	current := runewidth.StringWidth(s)
	if current >= width {
		return s
	}
	return s + strings.Repeat(" ", width-current)
}

func separatorLine(width int) string {
	if width < 2 {
		return strings.Repeat("─", width)
	}
	return "├" + strings.Repeat("─", width-2) + "┤"
}

func formatInfoLine(text string, width int) string {
	if width < 2 {
		return padToWidth(text, width)
	}
	return "│" + padToWidth(text, width-2) + "│"
}

// BlockInfo represents current block information
type BlockInfo struct {
	Height          int64
	Hash            string
	Time            time.Time
	Proposer        string
	Moniker         string
	BlockTime       time.Duration // Time since last block
	AvgBlockTime    time.Duration // Average block time
	ConsensusHeight int64
	Round           int32
	ChainID         string
	Tendermint      string
}

// VoteStatus represents the status of a vote
type VoteStatus int

const (
	VoteStatusNone  VoteStatus = iota // No vote
	VoteStatusNil                     // Vote with nil/zero blockhash
	VoteStatusValid                   // Vote with non-zero blockhash
)

// ValidatorInfo represents a validator
type ValidatorInfo struct {
	Address      string
	Moniker      string
	VotingPower  int64
	PowerPercent float64
	PreVote      VoteStatus // PreVote status
	PreCommit    VoteStatus // PreCommit status
}

// UpdateMsg is sent when block info should be updated
type UpdateMsg struct {
	Block BlockInfo
}

// ValidatorsUpdateMsg is sent when validators list should be updated
type ValidatorsUpdateMsg struct {
	Validators []ValidatorInfo
}

// Model holds the TUI state
type Model struct {
	currentBlock BlockInfo
	validators   []ValidatorInfo
	width        int
	height       int
}

// NewModel creates a new TUI model
func NewModel() Model {
	return Model{
		currentBlock: BlockInfo{
			Height:          0,
			Hash:            "",
			Time:            time.Time{},
			Proposer:        "",
			Moniker:         "",
			ConsensusHeight: 0,
			Round:           0,
		},
		validators: []ValidatorInfo{},
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return nil
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case UpdateMsg:
		m.currentBlock = msg.Block
		return m, nil

	case ValidatorsUpdateMsg:
		m.validators = msg.Validators
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	}

	return m, nil
}

// View renders the UI
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Calculate block time
	blockTimeStr := "0s"
	if m.currentBlock.BlockTime > 0 {
		blockTimeStr = fmt.Sprintf("%.3fs", m.currentBlock.BlockTime.Seconds())
	}

	// Format hash (truncate if too long)
	hashStr := m.currentBlock.Hash
	if len(hashStr) > 16 {
		hashStr = hashStr[:16] + "..."
	}

	// Format proposer (truncate if too long)
	proposerStr := m.currentBlock.Proposer
	if len(proposerStr) > 16 {
		proposerStr = proposerStr[:16] + "..."
	}

	// Format average block time
	avgBlockTimeStr := "N/A"
	if m.currentBlock.AvgBlockTime > 0 {
		avgBlockTimeStr = fmt.Sprintf("%.3fs", m.currentBlock.AvgBlockTime.Seconds())
	}

	// Build the header section (similar to tmtop format)
	header := m.renderHeader(
		blockTimeStr,
		avgBlockTimeStr,
		hashStr,
		proposerStr,
		m.currentBlock.Moniker,
		m.currentBlock.Height,
		m.currentBlock.ConsensusHeight,
		m.currentBlock.Round,
		m.currentBlock.ChainID,
		m.currentBlock.Tendermint,
	)

	// Build the validators table
	validatorsTable := m.renderValidators()

	// Combine everything
	return lipgloss.JoinVertical(lipgloss.Left, header, validatorsTable)
}

// renderHeader renders the top header section
func (m Model) renderHeader(blockTimeStr, avgBlockTimeStr, hashStr, proposerStr, moniker string, committedHeight, consensusHeight int64, round int32, chainID, tendermint string) string {
	// Calculate column widths (approximately 1/3 each, accounting for borders)
	colWidth := (m.width - 4) / 3
	rightColWidth := m.width - colWidth*2 - 4

	// Left column: Block info
	leftLines := []string{
		fmt.Sprintf("committed=%d consensus=%d round=%d", committedHeight, consensusHeight, round),
		fmt.Sprintf("block time: %s", blockTimeStr),
		fmt.Sprintf("hash: %s", hashStr),
		fmt.Sprintf("proposer: %s (%s)", proposerStr, moniker),
	}

	// Middle column: Chain info (placeholder for now)
	chainLine := "chain name: N/A"
	if chainID != "" {
		chainLine = fmt.Sprintf("chain name: %s", chainID)
	}
	tmLine := "tendermint version: N/A"
	if tendermint != "" {
		tmLine = fmt.Sprintf("tendermint version: %s", tendermint)
	}

	middleLines := []string{
		chainLine,
		tmLine,
		fmt.Sprintf("avg block time: %s", avgBlockTimeStr),
	}

	// Right column: Progress bars (placeholder for now)
	rightLines := []string{
		"",
		"",
		"",
		"",
	}

	// Build rows
	var rows []string
	maxLines := len(leftLines)
	if len(middleLines) > maxLines {
		maxLines = len(middleLines)
	}
	if len(rightLines) > maxLines {
		maxLines = len(rightLines)
	}

	for i := 0; i < maxLines; i++ {
		left := ""
		if i < len(leftLines) {
			left = leftLines[i]
		}
		middle := ""
		if i < len(middleLines) {
			middle = middleLines[i]
		}
		right := ""
		if i < len(rightLines) {
			right = rightLines[i]
		}

		// Truncate if too long
		if len(left) > colWidth {
			left = left[:colWidth-3] + "..."
		}
		if len(middle) > colWidth {
			middle = middle[:colWidth-3] + "..."
		}
		if len(right) > rightColWidth {
			right = right[:rightColWidth-3] + "..."
		}

		// Pad strings to fit column width
		leftPadded := left
		if len(leftPadded) < colWidth-2 {
			leftPadded = leftPadded + strings.Repeat(" ", colWidth-2-len(leftPadded))
		}
		middlePadded := middle
		if len(middlePadded) < colWidth-2 {
			middlePadded = middlePadded + strings.Repeat(" ", colWidth-2-len(middlePadded))
		}
		rightPadded := right
		if len(rightPadded) < rightColWidth-2 {
			rightPadded = rightPadded + strings.Repeat(" ", rightColWidth-2-len(rightPadded))
		}

		rows = append(rows, fmt.Sprintf("│ %s │ %s │ %s │",
			leftPadded,
			middlePadded,
			rightPadded))
	}

	// Build top border
	topBorder := fmt.Sprintf("┌%s┬%s┬%s┐",
		strings.Repeat("─", colWidth),
		strings.Repeat("─", colWidth),
		strings.Repeat("─", rightColWidth))

	// Build separator
	separator := fmt.Sprintf("├%s┴%s┴%s┤",
		strings.Repeat("─", colWidth),
		strings.Repeat("─", colWidth),
		strings.Repeat("─", rightColWidth))

	return topBorder + "\n" + strings.Join(rows, "\n") + "\n" + separator
}

// renderValidators renders the validators table
func (m Model) renderValidators() string {
	if len(m.validators) == 0 {
		return ""
	}

	// Calculate available height (subtract header height ~6 lines)
	availableHeight := m.height - 6
	if availableHeight <= 0 {
		return ""
	}

	// Display validators in columns (4 columns like in tmtop example)
	cols := 4

	// Calculate column width (accounting for borders: | col | col | col | col |)
	// We need: left border (1) + 4 columns + 3 separators (3) + right border (1) = width
	// So: 1 + colWidth*4 + 3 + 1 = width
	// colWidth*4 + 5 = width
	// colWidth = (width - 5) / 4
	// But we need to account for the actual width of separator characters
	separatorWidth := runewidth.StringWidth("│")
	borderWidth := runewidth.StringWidth("│") * 2       // left + right
	totalSeparatorsWidth := separatorWidth * (cols - 1) // separators between columns

	colWidth := (m.width - borderWidth - totalSeparatorsWidth) / cols
	if colWidth < 20 {
		colWidth = 20 // Minimum column width
	}

	// Calculate how many rows we can display
	maxRows := availableHeight - 2 // Subtract borders
	if maxRows <= 0 {
		return ""
	}

	// Calculate total rows needed
	totalRows := (len(m.validators) + cols - 1) / cols // Ceiling division

	// Limit rows to fit available height
	rows := totalRows
	if rows > maxRows {
		rows = maxRows
	}

	var lines []string

	formatRow := func(cells []string) string {
		for i, cell := range cells {
			cellWidth := runewidth.StringWidth(cell)
			if cellWidth < colWidth {
				cells[i] = cell + strings.Repeat(" ", colWidth-cellWidth)
			} else if cellWidth > colWidth {
				truncated := ""
				for _, r := range []rune(cell) {
					if runewidth.StringWidth(truncated+string(r)) >= colWidth {
						break
					}
					truncated += string(r)
				}
				cells[i] = truncated
			}
		}

		line := "│" + strings.Join(cells, "│") + "│"

		lineWidth := runewidth.StringWidth(line)
		if lineWidth < m.width {
			lineRunes := []rune(line)
			lastBorderIdx := len(lineRunes) - 1
			spacesNeeded := m.width - lineWidth
			line = string(lineRunes[:lastBorderIdx]) + strings.Repeat(" ", spacesNeeded) + string(lineRunes[lastBorderIdx:])
		} else if lineWidth > m.width {
			lineRunes := []rune(line)
			lastBorderIdx := len(lineRunes) - 1
			truncated := ""
			widthSoFar := 0
			for i, r := range lineRunes {
				if i == lastBorderIdx {
					truncated += string(r)
					break
				}
				runeWidth := runewidth.RuneWidth(r)
				if widthSoFar+runeWidth > m.width-1 {
					break
				}
				truncated += string(r)
				widthSoFar += runeWidth
			}
			line = truncated + "│"
		}

		return line
	}

	// Build rows
	for row := 0; row < rows; row++ {
		var rowCells []string
		for col := 0; col < cols; col++ {
			idx := row*cols + col
			if idx < len(m.validators) {
				val := m.validators[idx]
				// Format moniker
				moniker := val.Moniker
				if moniker == "" {
					// Use first 8 chars of address if no moniker
					if len(val.Address) > 8 {
						moniker = val.Address[:8] + "..."
					} else {
						moniker = val.Address
					}
				}

				// Get vote status symbols
				prevoteSymbol := getVoteSymbol(val.PreVote)
				precommitSymbol := getVoteSymbol(val.PreCommit)

				idStr := fmt.Sprintf("%3d", idx+1)
				powerPercent := fmt.Sprintf("%6.2f%%", val.PowerPercent)
				prefix := fmt.Sprintf("%s %s %s %s ", idStr, powerPercent, prevoteSymbol, precommitSymbol)
				prefixWidth := runewidth.StringWidth(prefix)
				availableForMoniker := colWidth - prefixWidth
				if availableForMoniker < 1 {
					availableForMoniker = 1
				}

				// Truncate moniker if too long (using display width, not rune count)
				monikerWidth := runewidth.StringWidth(moniker)
				if monikerWidth > availableForMoniker {
					// Truncate by display width
					truncated := ""
					for _, r := range []rune(moniker) {
						if runewidth.StringWidth(truncated+string(r)) > availableForMoniker-3 {
							break
						}
						truncated += string(r)
					}
					moniker = truncated + "..."
				}

				cell := prefix + moniker
				rowCells = append(rowCells, cell)
			} else {
				// Empty cell - pad to exact width (just spaces)
				rowCells = append(rowCells, strings.Repeat(" ", colWidth))
			}
		}

		lines = append(lines, formatRow(rowCells))
	}

	// Build borders
	topBorder := "├" + strings.Repeat("─", m.width-2) + "┤"
	bottomBorder := "└" + strings.Repeat("─", m.width-2) + "┘"

	return topBorder + "\n" + strings.Join(lines, "\n") + "\n" + separatorLine(m.width) + "\n" + formatInfoLine("ID, Voting Power, PreVote, PreCommit, Moniker", m.width) + "\n" + bottomBorder
}

// getVoteSymbol returns the symbol for a vote status
func getVoteSymbol(status VoteStatus) string {
	switch status {
	case VoteStatusNone:
		return "❌"
	case VoteStatusNil:
		return "🤷"
	case VoteStatusValid:
		return "✅"
	default:
		return "❌"
	}
}

// Run starts the TUI program
func Run(updateCh <-chan interface{}) error {
	m := NewModel()
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Start goroutine to receive updates
	go func() {
		for data := range updateCh {
			// Try to convert to BlockInfo
			if blockInfo, ok := data.(BlockInfo); ok {
				p.Send(UpdateMsg{Block: blockInfo})
			} else if validators, ok := data.([]ValidatorInfo); ok {
				p.Send(ValidatorsUpdateMsg{Validators: validators})
			} else if validatorsSlice, ok := data.([]struct {
				Address      string
				Moniker      string
				VotingPower  int64
				PowerPercent float64
				PreVote      int
				PreCommit    int
			}); ok {
				// Convert slice of anonymous structs to []ValidatorInfo
				validators := make([]ValidatorInfo, len(validatorsSlice))
				for i, v := range validatorsSlice {
					validators[i] = ValidatorInfo{
						Address:      v.Address,
						Moniker:      v.Moniker,
						VotingPower:  v.VotingPower,
						PowerPercent: v.PowerPercent,
						PreVote:      VoteStatus(v.PreVote),
						PreCommit:    VoteStatus(v.PreCommit),
					}
				}
				p.Send(ValidatorsUpdateMsg{Validators: validators})
			} else {
				// Try to convert from anonymous struct for BlockInfo
				if v, ok := data.(struct {
					Height          int64
					Hash            string
					Time            time.Time
					Proposer        string
					Moniker         string
					BlockTime       time.Duration
					AvgBlockTime    time.Duration
					ConsensusHeight int64
					Round           int32
				}); ok {
					blockInfo := BlockInfo{
						Height:          v.Height,
						Hash:            v.Hash,
						Time:            v.Time,
						Proposer:        v.Proposer,
						Moniker:         v.Moniker,
						BlockTime:       v.BlockTime,
						AvgBlockTime:    v.AvgBlockTime,
						ConsensusHeight: v.ConsensusHeight,
						Round:           v.Round,
					}
					p.Send(UpdateMsg{Block: blockInfo})
				} else if v, ok := data.(struct {
					Height       int64
					Hash         string
					Time         time.Time
					Proposer     string
					Moniker      string
					BlockTime    time.Duration
					AvgBlockTime time.Duration
				}); ok {
					blockInfo := BlockInfo{
						Height:       v.Height,
						Hash:         v.Hash,
						Time:         v.Time,
						Proposer:     v.Proposer,
						Moniker:      v.Moniker,
						BlockTime:    v.BlockTime,
						AvgBlockTime: v.AvgBlockTime,
					}
					p.Send(UpdateMsg{Block: blockInfo})
				} else if v, ok := data.(struct {
					Validators []ValidatorInfo
				}); ok {
					p.Send(ValidatorsUpdateMsg{Validators: v.Validators})
				}
			}
		}
		// Channel closed, quit TUI
		p.Quit()
	}()

	_, err := p.Run()
	return err
}
