-- +goose Up
CREATE TABLE chat_actors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('human','bot')),
    external_id text,
    display_name text NOT NULL,
    avatar_url text,
    avatar_source text,
    home_device_id text,
    disabled boolean NOT NULL DEFAULT false,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_actors_account_idx ON chat_actors (account_id);
CREATE UNIQUE INDEX chat_actors_external_uniq ON chat_actors (account_id, external_id) WHERE external_id IS NOT NULL;

CREATE TABLE chat_rooms (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('group','direct')),
    owner_account_id uuid NOT NULL,
    name text NOT NULL DEFAULT '',
    topic text,
    description text,
    response_mode text,
    speaking_mode text,
    organizer_id uuid,
    revision int NOT NULL DEFAULT 1,
    head bigint NOT NULL DEFAULT 0,
    archived boolean NOT NULL DEFAULT false,
    dissolved_at timestamptz,
    source_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_rooms_owner_idx ON chat_rooms (owner_account_id, updated_at DESC);
CREATE UNIQUE INDEX chat_rooms_direct_uniq ON chat_rooms (owner_account_id, source_id) WHERE kind = 'direct' AND source_id IS NOT NULL;

CREATE TABLE chat_room_members (
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    role text NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member','guest')),
    state text NOT NULL DEFAULT 'joined' CHECK (state IN ('joined','left','removed','banned','invited')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    last_read_seq bigint NOT NULL DEFAULT 0,
    notification_level text NOT NULL DEFAULT 'all',
    guest_access text NOT NULL DEFAULT 'none',
    access_revision int NOT NULL DEFAULT 1,
    nickname text,
    position int NOT NULL DEFAULT 0,
    removed_at timestamptz,
    PRIMARY KEY (room_id, actor_id)
);
CREATE INDEX chat_members_actor_idx ON chat_room_members (actor_id, state);

CREATE TABLE chat_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    seq bigint NOT NULL,
    sender_actor_id uuid NOT NULL,
    client_message_id text,
    kind text NOT NULL DEFAULT 'message',
    content jsonb NOT NULL DEFAULT '[]'::jsonb,
    reply_to_id uuid,
    thread_root_id uuid,
    source_id text,
    source jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    edited_at timestamptz,
    deleted_at timestamptz,
    UNIQUE (room_id, seq)
);
CREATE INDEX chat_messages_room_seq_idx ON chat_messages (room_id, seq DESC);
CREATE INDEX chat_messages_thread_idx ON chat_messages (thread_root_id, seq);
CREATE UNIQUE INDEX chat_messages_client_uniq ON chat_messages (room_id, client_message_id) WHERE client_message_id IS NOT NULL;
CREATE UNIQUE INDEX chat_messages_source_uniq ON chat_messages (room_id, source_id) WHERE source_id IS NOT NULL;

CREATE TABLE chat_message_reactions (
    message_id uuid NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    emoji text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, actor_id, emoji)
);

CREATE TABLE chat_media (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL,
    uploader_actor_id uuid NOT NULL,
    oss_key text NOT NULL,
    mime text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sealed')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_media_room_idx ON chat_media (room_id, created_at DESC);

CREATE TABLE chat_attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id uuid NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
    media_id uuid,
    oss_key text NOT NULL,
    mime text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text,
    original_name text
);

CREATE TABLE chat_executions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    source_message_id uuid,
    bot_actor_id uuid NOT NULL,
    requester_id uuid,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','claimed','running','completed','failed','stopped')),
    failure_code text,
    plan_id uuid,
    plan_step int,
    context_seq bigint,
    attachment_after_seq bigint,
    epoch int NOT NULL DEFAULT 0,
    access_mode text NOT NULL DEFAULT 'chat',
    access_revision int NOT NULL DEFAULT 1,
    lease_expires_at timestamptz,
    error jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_executions_room_idx ON chat_executions (room_id, created_at DESC);
CREATE INDEX chat_executions_bot_idx ON chat_executions (bot_actor_id, status);

CREATE TABLE chat_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    revision int NOT NULL DEFAULT 1,
    source_message_id uuid,
    request_text text,
    organizer_id uuid,
    creator_id uuid,
    status text NOT NULL DEFAULT 'pending',
    current_step int,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chat_plan_steps (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id uuid NOT NULL REFERENCES chat_plans(id) ON DELETE CASCADE,
    position int NOT NULL,
    bot_actor_id uuid NOT NULL,
    task text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    result_message_id uuid
);
CREATE UNIQUE INDEX chat_plan_steps_pos_uniq ON chat_plan_steps (plan_id, position);

CREATE TABLE chat_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chat_room_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id uuid NOT NULL REFERENCES chat_rooms(id) ON DELETE CASCADE,
    seq bigint NOT NULL,
    type text NOT NULL,
    actor_id uuid,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (room_id, seq)
);

CREATE TABLE chat_companions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL,
    bot_actor_id uuid NOT NULL,
    main_device_id text,
    status text NOT NULL DEFAULT 'active',
    profile_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX chat_companions_actor_uniq ON chat_companions (bot_actor_id);

CREATE TABLE chat_bot_groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_account_id uuid NOT NULL,
    name text NOT NULL,
    avatar_url text,
    status text NOT NULL DEFAULT 'active',
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chat_bot_group_members (
    group_id uuid NOT NULL REFERENCES chat_bot_groups(id) ON DELETE CASCADE,
    actor_id uuid NOT NULL,
    role text NOT NULL DEFAULT 'member',
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, actor_id)
);

CREATE TABLE chat_preferences (
    actor_id uuid NOT NULL,
    key text NOT NULL,
    value jsonb,
    revision int NOT NULL DEFAULT 1,
    deleted_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (actor_id, key)
);

CREATE TABLE chat_list_state (
    room_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    hidden boolean NOT NULL DEFAULT false,
    revision int NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, actor_id)
);

-- +goose Down
DROP TABLE IF EXISTS chat_list_state;
DROP TABLE IF EXISTS chat_preferences;
DROP TABLE IF EXISTS chat_bot_group_members;
DROP TABLE IF EXISTS chat_bot_groups;
DROP TABLE IF EXISTS chat_companions;
DROP TABLE IF EXISTS chat_room_events;
DROP TABLE IF EXISTS chat_invitations;
DROP TABLE IF EXISTS chat_plan_steps;
DROP TABLE IF EXISTS chat_plans;
DROP TABLE IF EXISTS chat_executions;
DROP TABLE IF EXISTS chat_attachments;
DROP TABLE IF EXISTS chat_media;
DROP TABLE IF EXISTS chat_message_reactions;
DROP TABLE IF EXISTS chat_messages;
DROP TABLE IF EXISTS chat_room_members;
DROP TABLE IF EXISTS chat_rooms;
DROP TABLE IF EXISTS chat_actors;
