-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Quay-compatible API facade (internal/quayapi, docs/integrations/konflux-image-controller.md).
-- A Quay organization is a hosted docker repository; the tables below hold what
-- a Quay client creates inside it:
--   quay_repositories  an image path (<namespace>/<component>) with the two
--                      permission targets that scope a robot to that image;
--   quay_robots        a robot account: the service account behind it and its
--                      token, sealed with OPENARTIFACT_SECRET_KEY because the
--                      Quay API returns a robot token again on every GET;
--   quay_teams         a Quay team: the group behind it;
--   quay_notifications repository notifications, stored and served back.
-- None of them is journaled (internal/recovery NotJournaled): they are
-- integration state that the image-controller recreates on its next reconcile.
SET LOCAL lock_timeout = '10s';

CREATE TABLE public.quay_repositories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT '' NOT NULL,
    read_target_id uuid NOT NULL,
    write_target_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT quay_repositories_pkey PRIMARY KEY (id),
    CONSTRAINT quay_repositories_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT quay_repositories_read_target_id_fkey FOREIGN KEY (read_target_id) REFERENCES public.permission_targets(id) ON DELETE CASCADE,
    CONSTRAINT quay_repositories_write_target_id_fkey FOREIGN KEY (write_target_id) REFERENCES public.permission_targets(id) ON DELETE CASCADE,
    CONSTRAINT quay_repositories_name_key UNIQUE (repository_id, name),
    CONSTRAINT quay_repositories_name_check CHECK (length(name) BETWEEN 1 AND 255),
    CONSTRAINT quay_repositories_description_check CHECK (length(description) <= 4096)
);

CREATE INDEX idx_quay_repositories_read_target ON public.quay_repositories USING btree (read_target_id);
CREATE INDEX idx_quay_repositories_write_target ON public.quay_repositories USING btree (write_target_id);

CREATE TABLE public.quay_robots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT '' NOT NULL,
    user_id uuid NOT NULL,
    token_id uuid,
    token_enc text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT quay_robots_pkey PRIMARY KEY (id),
    CONSTRAINT quay_robots_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT quay_robots_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.auth_users(id) ON DELETE CASCADE,
    CONSTRAINT quay_robots_name_key UNIQUE (repository_id, name),
    CONSTRAINT quay_robots_user_id_key UNIQUE (user_id),
    CONSTRAINT quay_robots_name_check CHECK (length(name) BETWEEN 1 AND 255),
    CONSTRAINT quay_robots_description_check CHECK (length(description) <= 4096)
);

CREATE TABLE public.quay_teams (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    group_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT quay_teams_pkey PRIMARY KEY (id),
    CONSTRAINT quay_teams_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT quay_teams_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.auth_groups(id) ON DELETE CASCADE,
    CONSTRAINT quay_teams_name_key UNIQUE (repository_id, name),
    CONSTRAINT quay_teams_group_id_key UNIQUE (group_id),
    CONSTRAINT quay_teams_name_check CHECK (length(name) BETWEEN 1 AND 255)
);

CREATE TABLE public.quay_notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    quay_repository_id uuid NOT NULL,
    title text NOT NULL,
    event text NOT NULL,
    method text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    event_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT quay_notifications_pkey PRIMARY KEY (id),
    CONSTRAINT quay_notifications_quay_repository_id_fkey FOREIGN KEY (quay_repository_id) REFERENCES public.quay_repositories(id) ON DELETE CASCADE,
    CONSTRAINT quay_notifications_title_check CHECK (length(title) <= 255),
    CONSTRAINT quay_notifications_event_check CHECK (length(event) BETWEEN 1 AND 64),
    CONSTRAINT quay_notifications_method_check CHECK (length(method) BETWEEN 1 AND 64)
);

CREATE INDEX idx_quay_notifications_repository ON public.quay_notifications USING btree (quay_repository_id, created_at, id);
