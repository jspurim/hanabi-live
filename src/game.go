package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/Zamiell/hanabi-live/src/models"
)

type Game struct {
	ID                 int
	Name               string
	Owner              int    // The user ID of the person who started the game or the current leader of the shared replay
	Password           string // This is a salted SHA512 hash sent by the client, but it can technically be any string at all
	Options            *Options
	Players            []*Player
	Spectators         []*Session
	DisconSpectators   map[int]bool
	Running            bool
	SharedReplay       bool
	DatetimeCreated    time.Time
	DatetimeLastAction time.Time
	DatetimeStarted    time.Time
	DatetimeFinished   time.Time
	EndCondition       int // See "database_schema.sql" for mappings

	Seed            string
	Deck            []*Card
	DeckIndex       int
	Stacks          []int
	StackDirections []int // The possible values for this are listed in "constants.go"
	Turn            int
	TurnsInverted   bool
	ActivePlayer    int
	Clues           int
	Score           int
	MaxScore        int
	Progress        int
	Strikes         int
	Actions         []Action // We don't want this to be a pointer because this simplifies scrubbing
	Sound           string
	TurnBeginTime   time.Time
	EndPlayer       int                // Set when the final card is drawn to determine when the game should end
	EndTurn         int                // Set when the game ends (to be used in Shared Replays)
	BlindPlays      int                // The number of consecutive blind plays
	Chat            []*GameChatMessage // All of the in-game chat history
}

type Options struct {
	Variant              int
	Timed                bool
	TimeBase             float64
	TimePerTurn          int
	DeckPlays            bool
	EmptyClues           bool
	CharacterAssignments bool
}

type GameChatMessage struct {
	UserID       int
	Username     string
	Msg          string
	DatetimeSent time.Time
}

/*
	Miscellaneous functions
*/

func (g *Game) GetName() string {
	return "Game #" + strconv.Itoa(g.ID) + " (" + g.Name + ") - Turn " + strconv.Itoa(g.Turn) + " - "
}

func (g *Game) GetPlayerIndex(id int) int {
	// If this function is called for a replay, the game will be nil, so account for this
	if g == nil {
		return -1
	}

	for i, p := range g.Players {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (g *Game) GetSpectatorIndex(id int) int {
	// If this function is called for a replay, the game will be nil, so account for this
	if g == nil {
		return -1
	}

	for i, s := range g.Spectators {
		if s.UserID() == id {
			return i
		}
	}
	return -1
}

// UpdateMaxScore goes through the deck to see if needed cards have been discarded
func (g *Game) UpdateMaxScore() {
	// Don't bother adjusting the maximum score if we are playing a "Up or Down" variant,
	// as it is more difficult to calculate which cards are still needed
	if strings.HasPrefix(variants[g.Options.Variant].Name, "Up or Down") {
		return
	}

	g.MaxScore = 0
	for suit := range g.Stacks {
		for rank := 1; rank <= 5; rank++ {
			// Search through the deck to see if all the coipes of this card are discarded already
			cardAlive := false
			for _, c := range g.Deck {
				if c.Suit == suit && c.Rank == rank && !c.Discarded {
					cardAlive = true
					break
				}
			}
			if cardAlive {
				g.MaxScore++
			} else {
				break
			}
		}
	}
}

/*
	Notify functions
*/

// NotifyPlayerChange sends the people in the pre-game an update about the new amount of players
// This is only called in situations where the game has not started yet
func (g *Game) NotifyPlayerChange() {
	if g.Running {
		log.Error("The \"NotifyPlayerChange()\" function was called on a game that has already started.")
		return
	}

	for _, p := range g.Players {
		if !p.Present {
			continue
		}

		type GameMessage struct {
			Name                 string  `json:"name"`
			Running              bool    `json:"running"`
			NumPlayers           int     `json:"numPlayers"`
			Variant              int     `json:"variant"`
			Timed                bool    `json:"timed"`
			BaseTime             float64 `json:"baseTime"`
			TimePerTurn          int     `json:"timePerTurn"`
			ReorderCards         bool    `json:"reorderCards"`
			DeckPlays            bool    `json:"deckPlays"`
			EmptyClues           bool    `json:"emptyClues"`
			CharacterAssignments bool    `json:"characterAssignments"`
			Password             bool    `json:"password"`
			SharedReplay         bool    `json:"sharedReplay"`
		}
		p.Session.Emit("game", GameMessage{
			Name:                 g.Name,
			Running:              g.Running,
			NumPlayers:           len(g.Players),
			Variant:              g.Options.Variant,
			Timed:                g.Options.Timed,
			BaseTime:             g.Options.TimeBase,
			TimePerTurn:          g.Options.TimePerTurn,
			DeckPlays:            g.Options.DeckPlays,
			EmptyClues:           g.Options.EmptyClues,
			CharacterAssignments: g.Options.CharacterAssignments,
			Password:             g.Password != "",
			SharedReplay:         g.SharedReplay,
		})

		// Tell the client to redraw all of the lobby rectanges to account for the new player
		for j, p2 := range g.Players {
			if !p.Present {
				continue
			}

			type GamePlayerMessage struct {
				Index   int          `json:"index"`
				Name    string       `json:"name"`
				You     bool         `json:"you"`
				Present bool         `json:"present"`
				Stats   models.Stats `json:"stats"`
			}
			p.Session.Emit("gamePlayer", &GamePlayerMessage{
				Index:   j,
				Name:    p2.Name,
				You:     p.ID == p2.ID,
				Present: p2.Present,
				Stats:   p2.Stats,
			})
		}
	}
}

// NotifyTableReady disables or enables the "Start Game" button on the client
// This is only called in situations where the game has not started yet
func (g *Game) NotifyTableReady() {
	if g.Running {
		log.Error("The \"NotifyTableReady()\" function was called on a game that has already started.")
		return
	}

	for _, p := range g.Players {
		if p.ID != g.Owner {
			continue
		}

		if !p.Present {
			continue
		}

		type TableReadyMessage struct {
			Ready bool `json:"ready"`
		}
		p.Session.Emit("tableReady", &TableReadyMessage{
			Ready: len(g.Players) >= 2,
		})
		break
	}
}

// NotifyConnected will change the player name-tags different colors to indicate whether or not they are currently connected
// This is only called in situations where the game has started
func (g *Game) NotifyConnected() {
	if !g.Running {
		log.Error("The \"NotifyConnected()\" function was called on a game that has not started yet.")
		return
	}

	// Make a list of who is currently connected of the players in the current game
	list := make([]bool, 0)
	for _, p := range g.Players {
		list = append(list, p.Present)
	}

	// Send a "connected" message to all of the users in the game
	type ConnectedMessage struct {
		List []bool `json:"list"`
	}
	data := &ConnectedMessage{
		List: list,
	}

	// If this is a shared replay, then all of the players are also spectators, so we do not want to send them a duplicate message
	if !g.SharedReplay {
		for _, p := range g.Players {
			if !p.Present {
				continue
			}

			p.Session.Emit("connected", data)
		}
	}

	// Also send it to the spectators
	for _, s := range g.Spectators {
		s.Emit("connected", data)
	}
}

// NotifyAction sends the people in the game an update about the new action
// This is only called in situations where the game has started
func (g *Game) NotifyAction() {
	if !g.Running {
		log.Error("The \"NotifyAction()\" function was called on a game that has not started yet.")
		return
	}

	// Get the last action of the game
	a := g.Actions[len(g.Actions)-1]

	for _, p := range g.Players {
		if !p.Present {
			continue
		}

		p.Session.NotifyGameAction(a, g, p)
	}

	// Also send the spectators an update
	for _, s := range g.Spectators {
		s.NotifyGameAction(a, g, nil)
	}
}

func (g *Game) NotifySpectators() {
	// If this is a shared replay, then all of the players are also spectators, so we do not want to send them a duplicate message
	if !g.SharedReplay {
		for _, p := range g.Players {
			if !p.Present {
				continue
			}

			p.Session.NotifySpectators(g)
		}
	}

	for _, s := range g.Spectators {
		s.NotifySpectators(g)
	}
}

func (g *Game) NotifyTime() {
	for _, p := range g.Players {
		if !p.Present {
			continue
		}

		p.Session.NotifyClock(g)
	}

	for _, s := range g.Spectators {
		s.NotifyClock(g)
	}
}

func (g *Game) NotifySound() {
	type SoundMessage struct {
		File string `json:"file"`
	}

	// Send a sound notification
	for i, p := range g.Players {
		if !p.Present {
			continue
		}

		// Prepare the sound message
		sound := "turn_other"
		if g.Sound != "" {
			sound = g.Sound
		} else if i == g.ActivePlayer {
			sound = "turn_us"
		}
		data := &SoundMessage{
			File: sound,
		}
		p.Session.Emit("sound", data)
	}

	// Also send it to the spectators
	for _, s := range g.Spectators {
		// Prepare the sound message
		// (the code is duplicated here because I don't want to mess with
		// having to change the file name back to default)
		sound := "turn_other"
		if g.Sound != "" {
			sound = g.Sound
		}
		data := &SoundMessage{
			File: sound,
		}
		s.Emit("sound", data)
	}
}

func (g *Game) NotifyBoot() {
	// Boot the people in the game and/or shared replay back to the lobby screen
	type BootMessage struct {
		Type string `json:"type"`
	}
	msg := &BootMessage{
		Type: "boot",
	}

	if !g.SharedReplay {
		for _, p := range g.Players {
			if !p.Present {
				continue
			}

			p.Session.Emit("notify", msg)
		}
	}

	for _, s := range g.Spectators {
		s.Emit("notify", msg)
	}
}

func (g *Game) NotifySpectatorsNote(order int) {
	// Make an array that contains the notes for just this card
	notes := ""
	for _, p := range g.Players {
		notes += noteFormat(p.Name, p.Notes[order])
	}

	type NoteMessage struct {
		Order int    `json:"order"` // The order of the card in the deck that these notes correspond to
		Notes string `json:"notes"` // The combined notes for all the players, formatted by the server
	}
	data := &NoteMessage{
		Order: order,
		Notes: notes,
	}

	for _, s := range g.Spectators {
		s.Emit("note", data)
	}
}

/*
	Other major functions
*/

// CheckTimer is meant to be called in a new goroutine
func (g *Game) CheckTimer(turn int, p *Player) {
	// Sleep until the active player runs out of time
	time.Sleep(p.Time)
	commandMutex.Lock()
	defer commandMutex.Unlock()

	// Check to see if the game ended already
	if g.EndCondition > 0 {
		return
	}

	// Check to see if we have made a move in the meanwhile
	if turn != g.Turn {
		return
	}

	p.Time = 0
	log.Info(g.GetName() + "Time ran out for \"" + p.Name + "\".")

	// End the game
	d := &CommandData{
		Type: 4,
	}
	p.Session.Set("currentGame", g.ID)
	commandAction(p.Session, d)
}

func (g *Game) CheckEnd() bool {
	// Check for 3 strikes
	if g.Strikes == 3 {
		log.Info(g.GetName() + "3 strike maximum reached; ending the game.")
		g.EndCondition = 2
		return true
	}

	// Check to see if the final go-around has completed
	// (which is initiated after the last card is played from the deck)
	// We can't use the amount of turns to determine this because certain characters can each player to take multiple turns
	if g.ActivePlayer == g.EndPlayer {
		allDoneFinalTurn := true
		for _, p := range g.Players {
			if !p.PerformedFinalTurn {
				allDoneFinalTurn = false
				break
			}
		}
		if allDoneFinalTurn {
			log.Info(g.GetName() + "Final turn reached; ending the game.")
			g.EndCondition = 1
			return true
		}
	}

	// Check to see if the maximum score has been reached
	if g.Score == g.MaxScore {
		log.Info(g.GetName() + "Maximum score reached; ending the game.")
		g.EndCondition = 1
		return true
	}

	// Check to see if there are any cards remaining that can be played on the stacks
	if strings.HasPrefix(variants[g.Options.Variant].Name, "Up or Down") {
		// Don't bother searching through the deck if we are playing an "Up or Down" variant,
		// as it is more difficult to calculate which cards are still needed
		return false
	}
	for i, stackLen := range g.Stacks {
		// Search through the deck
		neededSuit := i
		neededRank := stackLen + 1
		for _, c := range g.Deck {
			if c.Suit == neededSuit &&
				c.Rank == neededRank &&
				!c.Discarded {

				return false
			}
		}
	}

	// If we got this far, nothing can be played
	log.Info(g.GetName() + "No remaining cards can be played; ending the game.")
	g.EndCondition = 1
	return true
}

// CheckIdle is meant to be called in a new goroutine
func (g *Game) CheckIdle() {
	// Set the last action
	commandMutex.Lock()
	g.DatetimeLastAction = time.Now()
	log.Debug(g.GetName()+" Set last action to:", g.DatetimeLastAction)
	commandMutex.Unlock()

	// We want to clean up idle games, so sleep for a reasonable amount of time
	time.Sleep(idleGameTimeout + time.Second)
	commandMutex.Lock()
	defer commandMutex.Unlock()

	// Check to see if the game still exists
	if _, ok := games[g.ID]; !ok {
		return
	}

	// Don't do anything if there has been an action in the meantime
	log.Debug(g.GetName()+" DatetimeLastAction:", g.DatetimeLastAction)
	log.Debug(g.GetName()+" TimeSince:", time.Since(g.DatetimeLastAction))
	if time.Since(g.DatetimeLastAction) < idleGameTimeout {
		return
	}

	log.Info(g.GetName() + " Idle timeout has elapsed; ending the game.")

	if g.SharedReplay {
		// If this is a shared replay, we want to send a message to the client that will take them back to the lobby
		g.NotifyBoot()
	}

	// Boot all of the spectators, if any
	for len(g.Spectators) > 0 {
		s := g.Spectators[0]
		s.Set("currentGame", g.ID)
		s.Set("status", "Spectating")
		commandGameUnattend(s, nil)
	}

	if g.SharedReplay {
		// If this is a shared replay, then we are done;
		// the shared should automatically end now that all of the spectators have left
		return
	}

	// Get the session of the owner
	var s *Session
	for _, p := range g.Players {
		if p.Session.UserID() == g.Owner {
			s = p.Session
			break
		}
	}

	if g.Running {
		// We need to end a game that has started
		// (this will put everyone in a non-shared replay of the idle game)
		d := &CommandData{
			Type: actionTypeIdleLimitReached,
		}
		s.Set("currentGame", g.ID)
		s.Set("status", "Playing")
		commandAction(s, d)
	} else {
		// We need to end a game that hasn't started yet
		// Force the owner to leave, which should subsequently eject everyone else
		// (this will send everyone back to the main lobby screen)
		s.Set("currentGame", g.ID)
		s.Set("status", "Pre-Game")
		commandGameLeave(s, nil)
	}
}
