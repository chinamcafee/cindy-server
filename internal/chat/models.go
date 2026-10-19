package chat

import (
	"encoding/json"
	"time"
)

type rowScanner interface {
	Scan(dest ...any) error
}

type actorRow struct {
	ID           string
	AccountID    string
	Kind         string
	ExternalID   *string
	DisplayName  string
	AvatarURL    *string
	AvatarSource *string
	HomeDeviceID *string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const actorCols = `id, account_id, kind, external_id, display_name, avatar_url, avatar_source, home_device_id, disabled, created_at, updated_at`

func scanActor(row rowScanner) (actorRow, error) {
	var a actorRow
	err := row.Scan(&a.ID, &a.AccountID, &a.Kind, &a.ExternalID, &a.DisplayName, &a.AvatarURL,
		&a.AvatarSource, &a.HomeDeviceID, &a.Disabled, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

type actorWire struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	ExternalID   *string `json:"externalId"`
	Name         string  `json:"name"`
	Avatar       *string `json:"avatar"`
	AvatarSource *string `json:"avatarSource"`
	HomeDeviceID *string `json:"homeDeviceId"`
	Disabled     bool    `json:"disabled"`
}

func (a actorRow) wire() actorWire {
	return actorWire{
		ID: a.ID, Kind: a.Kind, ExternalID: a.ExternalID, Name: a.DisplayName,
		Avatar: a.AvatarURL, AvatarSource: a.AvatarSource, HomeDeviceID: a.HomeDeviceID, Disabled: a.Disabled,
	}
}

type roomRow struct {
	ID             string
	Kind           string
	OwnerAccountID string
	Name           string
	Topic          *string
	Description    *string
	ResponseMode   *string
	SpeakingMode   *string
	OrganizerID    *string
	Revision       int
	Head           int64
	Archived       bool
	DissolvedAt    *time.Time
	SourceID       *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const roomCols = `id, kind, owner_account_id, name, topic, description, response_mode, speaking_mode, organizer_id, revision, head, archived, dissolved_at, source_id, created_at, updated_at`

func scanRoom(row rowScanner) (roomRow, error) {
	var r roomRow
	err := row.Scan(&r.ID, &r.Kind, &r.OwnerAccountID, &r.Name, &r.Topic, &r.Description, &r.ResponseMode,
		&r.SpeakingMode, &r.OrganizerID, &r.Revision, &r.Head, &r.Archived, &r.DissolvedAt, &r.SourceID,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

type roomWire struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	Topic        *string    `json:"topic"`
	Description  *string    `json:"description"`
	ResponseMode *string    `json:"response_mode"`
	SpeakingMode *string    `json:"speaking_mode"`
	OrganizerID  *string    `json:"organizer_id"`
	Revision     int        `json:"revision"`
	Head         string     `json:"head"`
	Archived     bool       `json:"archived"`
	DissolvedAt  *time.Time `json:"dissolved_at"`
	State        string     `json:"state,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (r roomRow) wire(state string) roomWire {
	return roomWire{
		ID: r.ID, Kind: r.Kind, Name: r.Name, Topic: r.Topic, Description: r.Description,
		ResponseMode: r.ResponseMode, SpeakingMode: r.SpeakingMode, OrganizerID: r.OrganizerID,
		Revision: r.Revision, Head: formatSeq(r.Head), Archived: r.Archived, DissolvedAt: r.DissolvedAt,
		State: state, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

type memberRow struct {
	RoomID         string
	ActorID        string
	Role           string
	State          string
	GuestAccess    string
	AccessRevision int
	Nickname       *string
	Position       int
	LastReadSeq    int64
	JoinedAt       time.Time
}

type memberView struct {
	ActorID        string
	Kind           string
	DisplayName    string
	Nickname       *string
	Avatar         *string
	State          string
	Role           string
	GuestAccess    string
	AccessRevision int
	OwnerActorID   *string
	OwnerName      *string
}

type memberWire struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Name           string  `json:"name"`
	DisplayName    string  `json:"displayName"`
	Nickname       *string `json:"nickname"`
	Avatar         *string `json:"avatar"`
	State          string  `json:"state"`
	Role           string  `json:"role"`
	OwnerActorID   *string `json:"ownerActorId"`
	OwnerName      *string `json:"ownerName"`
	GuestAccess    string  `json:"guestAccess"`
	AccessRevision int     `json:"accessRevision"`
}

func (v memberView) wire() memberWire {
	return memberWire{
		ID: v.ActorID, Kind: v.Kind, Name: v.DisplayName, DisplayName: v.DisplayName, Nickname: v.Nickname,
		Avatar: v.Avatar, State: v.State, Role: v.Role, OwnerActorID: v.OwnerActorID, OwnerName: v.OwnerName,
		GuestAccess: v.GuestAccess, AccessRevision: v.AccessRevision,
	}
}

type messageRow struct {
	ID           string
	RoomID       string
	Seq          int64
	AuthorID     string
	AuthorKind   string
	AuthorName   string
	Content      []byte
	ReplyToID    *string
	ThreadRootID *string
	CreatedAt    time.Time
	Deleted      bool
}

const messageCols = `m.id, m.room_id, m.seq, m.sender_actor_id, a.kind, a.display_name, m.content, m.reply_to_id, m.thread_root_id, m.created_at, (m.deleted_at IS NOT NULL)`

func scanMessage(row rowScanner) (messageRow, error) {
	var msg messageRow
	err := row.Scan(&msg.ID, &msg.RoomID, &msg.Seq, &msg.AuthorID, &msg.AuthorKind, &msg.AuthorName, &msg.Content,
		&msg.ReplyToID, &msg.ThreadRootID, &msg.CreatedAt, &msg.Deleted)
	return msg, err
}

type messageAuthor struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type reactionWire struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
}

type messageWire struct {
	ID           string            `json:"id"`
	Seq          string            `json:"seq"`
	AuthorID     string            `json:"authorId"`
	Author       messageAuthor     `json:"author"`
	Content      []json.RawMessage `json:"content"`
	CreatedAt    time.Time         `json:"createdAt"`
	Deleted      bool              `json:"deleted"`
	ThreadRootID *string           `json:"threadRootId"`
	ReplyToID    *string           `json:"replyToId"`
	ReplyCount   int               `json:"replyCount,omitempty"`
	Reactions    []reactionWire    `json:"reactions,omitempty"`
}

func (m messageRow) wire() messageWire {
	content := []json.RawMessage{}
	if len(m.Content) > 0 {
		_ = json.Unmarshal(m.Content, &content)
	}
	if content == nil {
		content = []json.RawMessage{}
	}
	return messageWire{
		ID: m.ID, Seq: formatSeq(m.Seq), AuthorID: m.AuthorID,
		Author: messageAuthor{Kind: m.AuthorKind, Name: m.AuthorName}, Content: content,
		CreatedAt: m.CreatedAt, Deleted: m.Deleted, ThreadRootID: m.ThreadRootID, ReplyToID: m.ReplyToID,
	}
}

type executionRow struct {
	ID             string
	RoomID         string
	SourceMessage  *string
	BotActorID     string
	RequesterID    *string
	Status         string
	FailureCode    *string
	PlanID         *string
	PlanStep       *int
	ContextSeq     *int64
	Epoch          int
	AccessMode     string
	AccessRevision int
	LeaseExpiresAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const executionCols = `id, room_id, source_message_id, bot_actor_id, requester_id, status, failure_code, plan_id, plan_step, context_seq, epoch, access_mode, access_revision, lease_expires_at, created_at, updated_at`

func scanExecution(row rowScanner) (executionRow, error) {
	var e executionRow
	err := row.Scan(&e.ID, &e.RoomID, &e.SourceMessage, &e.BotActorID, &e.RequesterID, &e.Status, &e.FailureCode,
		&e.PlanID, &e.PlanStep, &e.ContextSeq, &e.Epoch, &e.AccessMode, &e.AccessRevision, &e.LeaseExpiresAt,
		&e.CreatedAt, &e.UpdatedAt)
	return e, err
}

type executionWire struct {
	ID                  string     `json:"id"`
	RoomID              string     `json:"roomId"`
	ConversationID      string     `json:"conversation_id"`
	SourceMessage       *string    `json:"sourceMessageId"`
	SourceMessageID     *string    `json:"source_message_id,omitempty"`
	BotID               string     `json:"botId"`
	BotIDSnake          string     `json:"bot_id"`
	RequesterID         *string    `json:"requesterId"`
	RequesterIDSnake    *string    `json:"requester_id,omitempty"`
	Status              string     `json:"status"`
	FailureCode         *string    `json:"failureCode"`
	FailureCodeSnake    *string    `json:"failure_code,omitempty"`
	PlanID              *string    `json:"planId"`
	PlanIDSnake         *string    `json:"plan_id,omitempty"`
	PlanStep            *int       `json:"planStep"`
	PlanStepSnake       *int       `json:"plan_step,omitempty"`
	ContextSeq          *string    `json:"contextSeq"`
	ContextSeqSnake     string     `json:"context_seq"`
	Epoch               int        `json:"epoch"`
	AccessMode          string     `json:"accessMode"`
	AccessModeSnake     string     `json:"access_mode"`
	AccessRevision      int        `json:"accessRevision"`
	AccessRevisionSnake int        `json:"access_revision"`
	LeaseExpiresAt      *time.Time `json:"leaseExpiresAt"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	UpdatedAtSnake      string     `json:"updated_at,omitempty"`
}

func (e executionRow) wire() executionWire {
	accessMode := e.AccessMode
	if accessMode == "" {
		accessMode = "owner"
	}
	accessRevision := e.AccessRevision
	if accessRevision <= 0 {
		accessRevision = 1
	}
	w := executionWire{
		ID: e.ID, RoomID: e.RoomID, ConversationID: e.RoomID,
		SourceMessage: e.SourceMessage, SourceMessageID: e.SourceMessage,
		BotID: e.BotActorID, BotIDSnake: e.BotActorID,
		RequesterID: e.RequesterID, RequesterIDSnake: e.RequesterID,
		Status: e.Status, FailureCode: e.FailureCode, FailureCodeSnake: e.FailureCode,
		PlanID: e.PlanID, PlanIDSnake: e.PlanID, PlanStep: e.PlanStep, PlanStepSnake: e.PlanStep,
		Epoch: e.Epoch, AccessMode: accessMode, AccessModeSnake: accessMode,
		AccessRevision: accessRevision, AccessRevisionSnake: accessRevision,
		LeaseExpiresAt: e.LeaseExpiresAt, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		UpdatedAtSnake: e.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if e.ContextSeq != nil {
		s := formatSeq(*e.ContextSeq)
		w.ContextSeq = &s
		w.ContextSeqSnake = s
	}
	return w
}

type planRow struct {
	ID            string
	RoomID        string
	SourceMessage *string
	RequestText   *string
	OrganizerID   *string
	CreatorID     *string
	Status        string
	CurrentStep   *int
	Revision      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const planCols = `id, room_id, source_message_id, request_text, organizer_id, creator_id, status, current_step, revision, created_at, updated_at`

func scanPlan(row rowScanner) (planRow, error) {
	var p planRow
	err := row.Scan(&p.ID, &p.RoomID, &p.SourceMessage, &p.RequestText, &p.OrganizerID, &p.CreatorID,
		&p.Status, &p.CurrentStep, &p.Revision, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

type planStepWire struct {
	ID              string  `json:"id"`
	Position        int     `json:"position"`
	BotID           string  `json:"botId"`
	BotName         string  `json:"botName"`
	Task            string  `json:"task"`
	Status          string  `json:"status"`
	ResultMessageID *string `json:"resultMessageId"`
}

type planWire struct {
	ID               string         `json:"id"`
	RoomID           string         `json:"roomId"`
	SourceMessage    *string        `json:"sourceMessageId"`
	SourceMessageID  *string        `json:"source_message_id,omitempty"`
	Request          *string        `json:"request"`
	RequestText      *string        `json:"request_text,omitempty"`
	OrganizerID      *string        `json:"organizerId"`
	OrganizerIDSnake *string        `json:"organizer_id,omitempty"`
	CreatorID        *string        `json:"creatorId"`
	CreatorIDSnake   *string        `json:"creator_id,omitempty"`
	Status           string         `json:"status"`
	CurrentStep      *int           `json:"currentStep"`
	CurrentStepSnake *int           `json:"current_step,omitempty"`
	Revision         int            `json:"revision"`
	CreatedAt        time.Time      `json:"createdAt"`
	CreatedAtSnake   string         `json:"created_at"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	UpdatedAtSnake   string         `json:"updated_at"`
	Steps            []planStepWire `json:"steps"`
}

func (p planRow) wire(steps []planStepWire) planWire {
	if steps == nil {
		steps = []planStepWire{}
	}
	return planWire{
		ID: p.ID, RoomID: p.RoomID, SourceMessage: p.SourceMessage, SourceMessageID: p.SourceMessage,
		Request: p.RequestText, RequestText: p.RequestText,
		OrganizerID: p.OrganizerID, OrganizerIDSnake: p.OrganizerID,
		CreatorID: p.CreatorID, CreatorIDSnake: p.CreatorID,
		Status: p.Status, CurrentStep: p.CurrentStep, CurrentStepSnake: p.CurrentStep,
		Revision: p.Revision, CreatedAt: p.CreatedAt, CreatedAtSnake: p.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: p.UpdatedAt, UpdatedAtSnake: p.UpdatedAt.UTC().Format(time.RFC3339Nano), Steps: steps,
	}
}
