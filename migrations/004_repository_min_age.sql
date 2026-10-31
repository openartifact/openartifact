-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Proxy repositories can hide versions the upstream published less than
-- min_age_hours ago (0 = off, at most 10 years).
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.repositories
    ADD COLUMN min_age_hours integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT repositories_min_age_hours_check CHECK (min_age_hours >= 0 AND min_age_hours <= 87600);
