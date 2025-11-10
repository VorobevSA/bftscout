package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

var proposerStyle = lipgloss.NewStyle().Background(lipgloss.Color("2")) // Green background

func padToWidth(s string, width int) string {
	current := runewidth.StringWidth(s)
	if current >= width {
		return s
	}
	return s + strings.Repeat(" ", width-current)
}

func truncateToWidth(s string, width int) string {
	if width <= 0 || runewidth.StringWidth(s) <= width {
		return s
	}
	const ellipsis = "..."
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	if width <= ellipsisWidth {
		var builder strings.Builder
		current := 0
		for _, r := range s {
			rw := runewidth.RuneWidth(r)
			if current+rw > width {
				break
			}
			builder.WriteRune(r)
			current += rw
		}
		return builder.String()
	}

	var builder strings.Builder
	current := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if current+rw > width-ellipsisWidth {
			break
		}
		builder.WriteRune(r)
		current += rw
	}
	return builder.String() + ellipsis
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
	Height                   int64
	Hash                     string
	Time                     time.Time
	Proposer                 string
	Moniker                  string
	BlockTime                time.Duration // Time since last block
	AvgBlockTime             time.Duration // Average block time
	ConsensusHeight          int64
	Round                    int32
	ChainID                  string
	CometBFT                 string
	PreVoteTotalPercent      float64 // Percentage of all validators who voted prevote (including nil)
	PreVoteWithHashPercent   float64 // Percentage of validators with non-nil prevote
	PreCommitTotalPercent    float64 // Percentage of all validators who voted precommit (including nil)
	PreCommitWithHashPercent float64 // Percentage of validators with non-nil precommit
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
	Address             string
	Moniker             string
	VotingPower         int64
	PowerPercent        float64
	ProposerSuccessRate float64
	NonNilVoteRate      float64
	HasProposerStats    bool
	HasVoteStats        bool
	IsCurrentProposer   bool
	PreVote             VoteStatus // PreVote status
	PreCommit           VoteStatus // PreCommit status
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
		m.currentBlock.ConsensusHeight,
		m.currentBlock.Round,
		m.currentBlock.ChainID,
		m.currentBlock.CometBFT,
		m.currentBlock.PreVoteTotalPercent,
		m.currentBlock.PreVoteWithHashPercent,
		m.currentBlock.PreCommitTotalPercent,
		m.currentBlock.PreCommitWithHashPercent,
	)

	// Build the validators table
	validatorsTable := m.renderValidators()

	// Combine everything
	return lipgloss.JoinVertical(lipgloss.Left, header, validatorsTable)
}

// renderProgressBar creates a progress bar with label and percentage (2 lines high)
// totalPercent: percentage of all votes (including nil) - shown in gray
// withHashPercent: percentage of votes with non-nil hash - shown in green (over gray)
func renderProgressBar(label string, totalPercent, withHashPercent float64, width int) []string {
	if totalPercent < 0 {
		totalPercent = 0
	}
	if totalPercent > 100 {
		totalPercent = 100
	}
	if withHashPercent < 0 {
		withHashPercent = 0
	}
	if withHashPercent > 100 {
		withHashPercent = 100
	}
	if withHashPercent > totalPercent {
		withHashPercent = totalPercent
	}

	// Calculate filled widths
	totalWidth := int(float64(width) * totalPercent / 100.0)
	if totalWidth > width {
		totalWidth = width
	}
	withHashWidth := int(float64(width) * withHashPercent / 100.0)
	if withHashWidth > width {
		withHashWidth = width
	}
	if withHashWidth > totalWidth {
		withHashWidth = totalWidth
	}

	// Format percentage text (show withHashPercent)
	percentText := fmt.Sprintf("%s: %.0f%%", label, withHashPercent)
	textWidth := runewidth.StringWidth(percentText)

	// Create styles: gray for all votes, green for votes with hash
	grayStyle := lipgloss.NewStyle().Background(lipgloss.Color("8"))  // Gray background
	greenStyle := lipgloss.NewStyle().Background(lipgloss.Color("2")) // Green background

	// Build text line centered (only for first line)
	spacesBefore := 0
	if textWidth < width {
		spacesBefore = (width - textWidth) / 2
	}
	textLine := strings.Repeat(" ", spacesBefore) + percentText + strings.Repeat(" ", width-spacesBefore-textWidth)

	// Build first line: gray background for total, green for withHash, text on top
	line1 := strings.Builder{}
	for i, r := range []rune(textLine) {
		if i < withHashWidth {
			// In green area (with hash) - apply green background
			line1.WriteString(greenStyle.Render(string(r)))
		} else if i < totalWidth {
			// In gray area (all votes but nil hash) - apply gray background
			line1.WriteString(grayStyle.Render(string(r)))
		} else {
			// In empty area - just the character
			line1.WriteRune(r)
		}
	}

	// Build second line: gray background for total, green for withHash, no text
	line2 := strings.Builder{}
	for i := 0; i < width; i++ {
		if i < withHashWidth {
			// In green area (with hash) - green background
			line2.WriteString(greenStyle.Render(" "))
		} else if i < totalWidth {
			// In gray area (all votes but nil hash) - gray background
			line2.WriteString(grayStyle.Render(" "))
		} else {
			// In empty area - just space
			line2.WriteString(" ")
		}
	}

	return []string{line1.String(), line2.String()}
}

// renderHeader renders the top header section
func (m Model) renderHeader(blockTimeStr, avgBlockTimeStr, hashStr, proposerStr, moniker string, consensusHeight int64, round int32, chainID, cometbft string, prevoteTotal, prevoteWithHash, precommitTotal, precommitWithHash float64) string {
	// Calculate column widths (approximately 1/3 each, accounting for borders)
	colWidth := (m.width - 4) / 3
	rightColWidth := m.width - colWidth*2 - 4

	// Progress bar width (use most of the right column width)
	progressBarWidth := rightColWidth - 2

	// Left column: Block info
	leftLines := []string{
		fmt.Sprintf("consensus=%d round=%d", consensusHeight, round),
		fmt.Sprintf("block time: %s", blockTimeStr),
		fmt.Sprintf("hash: %s", hashStr),
		fmt.Sprintf("proposer: %s (%s)", proposerStr, moniker),
	}

	// Middle column: Chain info
	chainLine := "chain name: N/A"
	if chainID != "" {
		chainLine = fmt.Sprintf("chain name: %s", chainID)
	}
	tmLine := "cometbft version: N/A"
	if cometbft != "" {
		tmLine = fmt.Sprintf("cometbft version: %s", cometbft)
	}

	middleLines := []string{
		chainLine,
		tmLine,
		fmt.Sprintf("avg block time: %s", avgBlockTimeStr),
	}

	// Right column: Progress bars (each is 2 lines, with 1 empty line between them)
	prevoteLines := renderProgressBar("Prevotes", prevoteTotal, prevoteWithHash, progressBarWidth)
	precommitLines := renderProgressBar("Precommits", precommitTotal, precommitWithHash, progressBarWidth)
	rightLines := []string{
		prevoteLines[0],   // First line of Prevotes
		prevoteLines[1],   // Second line of Prevotes
		"",                // Empty line between Prevotes and Precommits
		precommitLines[0], // First line of Precommits
		precommitLines[1], // Second line of Precommits
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
		leftWidth := colWidth - 2
		if leftWidth < 0 {
			leftWidth = 0
		}
		middleWidth := colWidth - 2
		if middleWidth < 0 {
			middleWidth = 0
		}
		rightWidth := rightColWidth - 2
		if rightWidth < 0 {
			rightWidth = 0
		}

		left = truncateToWidth(left, leftWidth)
		middle = truncateToWidth(middle, middleWidth)
		// For right column, use lipgloss.Width to handle ANSI codes correctly
		rightVisualWidth := lipgloss.Width(right)
		if rightVisualWidth > rightWidth {
			// Truncate if needed (simplified - just ensure it fits)
			right = truncateToWidth(right, rightWidth)
		}

		// Pad strings to fit column width
		leftPadded := padToWidth(left, leftWidth)
		middlePadded := padToWidth(middle, middleWidth)
		// For right column, calculate padding using lipgloss.Width to handle ANSI codes
		rightVisualWidth = lipgloss.Width(right)
		rightPadded := right
		if rightVisualWidth < rightWidth {
			rightPadded = right + strings.Repeat(" ", rightWidth-rightVisualWidth)
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
	if colWidth < 24 {
		colWidth = 24 // Minimum column width to fit stats
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

	formatRow := func(cells []string, proposerIndices []int) string {
		// First, format cells to correct width
		for i, cell := range cells {
			// Use lipgloss.Width() to measure width (handles ANSI codes correctly)
			cellWidth := lipgloss.Width(cell)
			if cellWidth < colWidth {
				cells[i] = cell + strings.Repeat(" ", colWidth-cellWidth)
			} else if cellWidth > colWidth {
				// Truncate if needed (shouldn't happen often)
				var builder strings.Builder
				widthSoFar := 0
				// Iterate by runes to handle Unicode correctly
				for _, r := range cell {
					runeWidth := runewidth.RuneWidth(r)
					if widthSoFar+runeWidth > colWidth {
						break
					}
					builder.WriteRune(r)
					widthSoFar += runeWidth
				}
				cells[i] = builder.String()
			}
		}

		// Apply green background style to proposer cells
		// This must be done after width formatting to ensure correct width
		for _, idx := range proposerIndices {
			if idx >= 0 && idx < len(cells) {
				// Use Width() to ensure the styled cell has exact colWidth
				cells[idx] = proposerStyle.Width(colWidth).Render(cells[idx])
			}
		}

		line := "│" + strings.Join(cells, "│") + "│"

		// Use lipgloss.Width() to correctly measure width with ANSI codes
		lineWidth := lipgloss.Width(line)
		if lineWidth < m.width {
			// Need to add spaces before the last border
			// Find the last border character position
			lastBorderPos := strings.LastIndex(line, "│")
			if lastBorderPos >= 0 {
				spacesNeeded := m.width - lineWidth
				line = line[:lastBorderPos] + strings.Repeat(" ", spacesNeeded) + line[lastBorderPos:]
			}
		}

		return line
	}

	// Build rows
	for row := 0; row < rows; row++ {
		var rowCells []string
		var proposerIndices []int // Track which cells are proposers
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
				proposerRate := "P: --%"
				if val.HasProposerStats {
					proposerRate = fmt.Sprintf("P:%5.1f%%", val.ProposerSuccessRate)
				}
				voteRate := "V: --%"
				if val.HasVoteStats {
					voteRate = fmt.Sprintf("V:%5.1f%%", val.NonNilVoteRate)
				}
				prefix := fmt.Sprintf("%s %s %s %s %s %s ", idStr, powerPercent, prevoteSymbol, precommitSymbol, proposerRate, voteRate)
				prefixWidth := runewidth.StringWidth(prefix)
				availableForMoniker := colWidth - prefixWidth
				if availableForMoniker < 1 {
					availableForMoniker = 1
				}

				// Truncate moniker if too long (using display width, not rune count)
				monikerWidth := runewidth.StringWidth(moniker)
				if monikerWidth > availableForMoniker {
					// Truncate by display width
					var builder strings.Builder
					for _, r := range moniker {
						if runewidth.StringWidth(builder.String()+string(r)) > availableForMoniker-3 {
							break
						}
						builder.WriteRune(r)
					}
					moniker = builder.String() + "..."
				}

				cell := prefix + moniker
				if val.IsCurrentProposer {
					proposerIndices = append(proposerIndices, len(rowCells))
				}
				rowCells = append(rowCells, cell)
			} else {
				// Empty cell - pad to exact width (just spaces)
				rowCells = append(rowCells, strings.Repeat(" ", colWidth))
			}
		}

		lines = append(lines, formatRow(rowCells, proposerIndices))
	}

	// Build borders
	topBorder := "├" + strings.Repeat("─", m.width-2) + "┤"
	bottomBorder := "└" + strings.Repeat("─", m.width-2) + "┘"

	return topBorder + "\n" + strings.Join(lines, "\n") + "\n" + separatorLine(m.width) + "\n" + formatInfoLine("ID, Voting Power, PreVote, PreCommit, Proposer %, Vote %, Moniker (green=current proposer)", m.width) + "\n" + bottomBorder
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
				Address             string
				Moniker             string
				VotingPower         int64
				PowerPercent        float64
				ProposerSuccessRate float64
				NonNilVoteRate      float64
				HasProposerStats    bool
				HasVoteStats        bool
				IsCurrentProposer   bool
				PreVote             int
				PreCommit           int
			}); ok {
				// Convert slice of anonymous structs to []ValidatorInfo
				validators := make([]ValidatorInfo, len(validatorsSlice))
				for i, v := range validatorsSlice {
					validators[i] = ValidatorInfo{
						Address:             v.Address,
						Moniker:             v.Moniker,
						VotingPower:         v.VotingPower,
						PowerPercent:        v.PowerPercent,
						ProposerSuccessRate: v.ProposerSuccessRate,
						NonNilVoteRate:      v.NonNilVoteRate,
						HasProposerStats:    v.HasProposerStats,
						HasVoteStats:        v.HasVoteStats,
						IsCurrentProposer:   v.IsCurrentProposer,
						PreVote:             VoteStatus(v.PreVote),
						PreCommit:           VoteStatus(v.PreCommit),
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
