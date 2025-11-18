// Package tui provides the terminal user interface for the consensus monitoring application.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const (
	// UI layout constants
	minUIWidth        = 2
	hashDisplayLen    = 16
	maxPercentValue   = 100
	percentMultiplier = 100.0
	uiBorderWidth     = 2
	uiColumnCount     = 3
	uiColumnSpacing   = 4
	uiMinColWidth     = 24
	uiHeaderHeight    = 6
	uiBorderCharWidth = 2
	addressDisplayLen = 8
	divisorTwo        = 2
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
	if width < minUIWidth {
		return strings.Repeat("─", width)
	}
	return "├" + strings.Repeat("─", width-uiBorderWidth) + "┤"
}

func formatInfoLine(text string, width int) string {
	if width < minUIWidth {
		return padToWidth(text, width)
	}
	return "│" + padToWidth(text, width-uiBorderWidth) + "│"
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
	// VoteStatusNone represents no vote.
	VoteStatusNone VoteStatus = iota
	// VoteStatusNil represents a vote with nil/zero blockhash.
	VoteStatusNil
	// VoteStatusValid represents a vote with non-zero blockhash.
	VoteStatusValid
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
	if len(hashStr) > hashDisplayLen {
		hashStr = hashStr[:hashDisplayLen] + "..."
	}

	// Format proposer (truncate if too long)
	proposerStr := m.currentBlock.Proposer
	if len(proposerStr) > hashDisplayLen {
		proposerStr = proposerStr[:hashDisplayLen] + "..."
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
// normalizePercent ensures percent is in valid range [0, maxPercentValue]
func normalizePercent(percent float64) float64 {
	if percent < 0 {
		return 0
	}
	if percent > maxPercentValue {
		return maxPercentValue
	}
	return percent
}

// calculateProgressWidths calculates the pixel widths for progress bars
func calculateProgressWidths(totalPercent, withHashPercent float64, width int) (totalWidth, withHashWidth int) {
	totalWidth = int(float64(width) * totalPercent / percentMultiplier)
	if totalWidth > width {
		totalWidth = width
	}
	withHashWidth = int(float64(width) * withHashPercent / percentMultiplier)
	if withHashWidth > width {
		withHashWidth = width
	}
	if withHashWidth > totalWidth {
		withHashWidth = totalWidth
	}
	return totalWidth, withHashWidth
}

// buildProgressLine renders a string with progress bar background highlighting
func buildProgressLine(textLine string, totalWidth, withHashWidth int, grayStyle, greenStyle lipgloss.Style) string {
	runes := []rune(textLine)
	line := strings.Builder{}
	for i, r := range runes {
		switch {
		case i < withHashWidth:
			line.WriteString(greenStyle.Render(string(r)))
		case i < totalWidth:
			line.WriteString(grayStyle.Render(string(r)))
		default:
			line.WriteRune(r)
		}
	}
	return line.String()
}

func centerText(text string, width int) string {
	if width <= 0 {
		return text
	}
	textWidth := runewidth.StringWidth(text)
	if textWidth >= width {
		return text
	}
	spacesBefore := (width - textWidth) / divisorTwo
	spacesAfter := width - spacesBefore - textWidth
	if spacesAfter < 0 {
		spacesAfter = 0
	}
	return strings.Repeat(" ", spacesBefore) + text + strings.Repeat(" ", spacesAfter)
}

func renderProgressBar(label string, totalPercent, withHashPercent float64, width int) []string {
	totalPercent = normalizePercent(totalPercent)
	withHashPercent = normalizePercent(withHashPercent)
	if withHashPercent > totalPercent {
		withHashPercent = totalPercent
	}

	totalWidth, withHashWidth := calculateProgressWidths(totalPercent, withHashPercent, width)

	labelText := centerText(fmt.Sprintf("%s:", label), width)

	infoText := centerText(fmt.Sprintf("Not null: %.0f%%  Total: %.0f%%", withHashPercent, totalPercent), width)

	grayStyle := lipgloss.NewStyle().Background(lipgloss.Color("8"))  // Gray background
	greenStyle := lipgloss.NewStyle().Background(lipgloss.Color("2")) // Green background

	line1 := buildProgressLine(labelText, totalWidth, withHashWidth, grayStyle, greenStyle)
	line2 := buildProgressLine(infoText, totalWidth, withHashWidth, grayStyle, greenStyle)

	return []string{line1, line2}
}

// renderHeader renders the top header section
func (m Model) renderHeader(blockTimeStr, avgBlockTimeStr, hashStr, proposerStr, moniker string, consensusHeight int64, round int32, chainID, cometbft string, prevoteTotal, prevoteWithHash, precommitTotal, precommitWithHash float64) string {
	// Calculate column widths (approximately 1/3 each, accounting for borders)
	colWidth := (m.width - uiColumnSpacing) / uiColumnCount
	rightColWidth := m.width - colWidth*(uiColumnCount-1) - uiColumnSpacing

	// Progress bar width (use most of the right column width)
	progressBarWidth := rightColWidth - uiBorderWidth

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
		leftWidth := colWidth - uiBorderWidth
		if leftWidth < 0 {
			leftWidth = 0
		}
		middleWidth := colWidth - uiBorderWidth
		if middleWidth < 0 {
			middleWidth = 0
		}
		rightWidth := rightColWidth - uiBorderWidth
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

	colWidth, rows, _, ok := m.calculateValidatorLayout()
	if !ok {
		return ""
	}

	var lines []string

	// Build rows
	for row := 0; row < rows; row++ {
		const validatorColumns = 4
		rowCells, proposerIndices := m.buildValidatorRow(row, validatorColumns, colWidth)
		lines = append(lines, formatValidatorRow(rowCells, proposerIndices, colWidth, m.width))
	}

	// Build borders
	topBorder := "├" + strings.Repeat("─", m.width-uiBorderWidth) + "┤"
	bottomBorder := "└" + strings.Repeat("─", m.width-uiBorderWidth) + "┘"

	return topBorder + "\n" + strings.Join(lines, "\n") + "\n" + separatorLine(m.width) + "\n" + formatInfoLine("ID, Voting Power, PreVote, PreCommit, Proposer %, Vote %, Moniker (green=current proposer)", m.width) + "\n" + bottomBorder
}

// buildValidatorRow builds a row of validator cells
func (m Model) buildValidatorRow(row, cols, colWidth int) ([]string, []int) {
	var rowCells []string
	var proposerIndices []int // Track which cells are proposers
	for col := 0; col < cols; col++ {
		idx := row*cols + col
		if idx < len(m.validators) {
			val := m.validators[idx]
			cell := m.formatValidatorCell(val, idx, colWidth)
			if val.IsCurrentProposer {
				proposerIndices = append(proposerIndices, len(rowCells))
			}
			rowCells = append(rowCells, cell)
		} else {
			// Empty cell - pad to exact width (just spaces)
			rowCells = append(rowCells, strings.Repeat(" ", colWidth))
		}
	}
	return rowCells, proposerIndices
}

// formatValidatorCell formats a single validator cell
func (m Model) formatValidatorCell(val ValidatorInfo, idx, colWidth int) string {
	// Format moniker
	moniker := m.formatMoniker(val)

	// Get vote status symbols
	prevoteSymbol := getVoteSymbol(val.PreVote)
	precommitSymbol := getVoteSymbol(val.PreCommit)

	// Format stats
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

	// Truncate moniker if too long
	moniker = m.truncateMoniker(moniker, availableForMoniker)

	return prefix + moniker
}

// formatMoniker formats moniker or uses address if moniker is empty
func (m Model) formatMoniker(val ValidatorInfo) string {
	if val.Moniker != "" {
		return val.Moniker
	}
	// Use first N chars of address if no moniker
	if len(val.Address) > addressDisplayLen {
		return val.Address[:addressDisplayLen] + "..."
	}
	return val.Address
}

// truncateMoniker truncates moniker to fit available width
func (m Model) truncateMoniker(moniker string, availableWidth int) string {
	monikerWidth := runewidth.StringWidth(moniker)
	if monikerWidth <= availableWidth {
		return moniker
	}
	// Truncate by display width
	var builder strings.Builder
	for _, r := range moniker {
		if runewidth.StringWidth(builder.String()+string(r)) > availableWidth-3 {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String() + "..."
}

// formatValidatorRow formats a row of validator cells
func formatValidatorRow(cells []string, proposerIndices []int, colWidth, totalWidth int) string {
	// First, format cells to correct width
	for i, cell := range cells {
		cellWidth := lipgloss.Width(cell)
		if cellWidth < colWidth {
			cells[i] = cell + strings.Repeat(" ", colWidth-cellWidth)
		} else if cellWidth > colWidth {
			cells[i] = truncateCell(cell, colWidth)
		}
	}

	// Apply green background style to proposer cells
	for _, idx := range proposerIndices {
		if idx >= 0 && idx < len(cells) {
			cells[idx] = proposerStyle.Width(colWidth).Render(cells[idx])
		}
	}

	line := "│" + strings.Join(cells, "│") + "│"

	// Adjust line width if needed
	lineWidth := lipgloss.Width(line)
	if lineWidth < totalWidth {
		lastBorderPos := strings.LastIndex(line, "│")
		if lastBorderPos >= 0 {
			spacesNeeded := totalWidth - lineWidth
			line = line[:lastBorderPos] + strings.Repeat(" ", spacesNeeded) + line[lastBorderPos:]
		}
	}

	return line
}

// truncateCell truncates a cell to fit width
func truncateCell(cell string, width int) string {
	var builder strings.Builder
	widthSoFar := 0
	for _, r := range cell {
		runeWidth := runewidth.RuneWidth(r)
		if widthSoFar+runeWidth > width {
			break
		}
		builder.WriteRune(r)
		widthSoFar += runeWidth
	}
	return builder.String()
}

// calculateValidatorLayout calculates layout parameters for validator display
func (m Model) calculateValidatorLayout() (colWidth, rows, maxRows int, ok bool) {
	// Calculate available height (subtract header height)
	availableHeight := m.height - uiHeaderHeight
	if availableHeight <= 0 {
		return 0, 0, 0, false
	}

	// Display validators in columns (4 columns like in tmtop example)
	cols := 4

	// Calculate column width
	separatorWidth := runewidth.StringWidth("│")
	borderWidth := runewidth.StringWidth("│") * uiBorderCharWidth // left + right
	totalSeparatorsWidth := separatorWidth * (cols - 1)           // separators between columns

	colWidth = (m.width - borderWidth - totalSeparatorsWidth) / cols
	if colWidth < uiMinColWidth {
		colWidth = uiMinColWidth // Minimum column width to fit stats
	}

	// Calculate how many rows we can display
	maxRows = availableHeight - uiBorderWidth // Subtract borders
	if maxRows <= 0 {
		return 0, 0, 0, false
	}

	// Calculate total rows needed
	totalRows := (len(m.validators) + cols - 1) / cols // Ceiling division

	// Limit rows to fit available height
	rows = totalRows
	if rows > maxRows {
		rows = maxRows
	}

	return colWidth, rows, maxRows, true
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
