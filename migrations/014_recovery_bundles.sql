-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Release bundles (and the promotion pipeline a bundle points at) are durable
-- user data that no blob store holds: a bundle item pins its blob by sha256
-- (blobUnreferencedExpr), so a rebuild that lost the items would let the next
-- orphan or cleanup pass delete blobs of sealed releases. The three tables join
-- the recovery journal; the reconcile backfills the existing rows (a table new
-- to the registry has never completed a pass).
SET LOCAL lock_timeout = '5s';

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.promotion_pipelines
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.release_bundles
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();

CREATE TRIGGER recovery_journal AFTER INSERT OR DELETE OR UPDATE ON public.release_bundle_items
    FOR EACH ROW EXECUTE FUNCTION public.recovery_trg_id();
