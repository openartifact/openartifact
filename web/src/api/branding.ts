// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { Branding, BrandingAdmin, BrandingUpdate } from './generated/types.gen'

export type { Branding, BrandingAdmin, BrandingUpdate }

export type BrandingAsset = 'logo-light' | 'logo-dark' | 'favicon'

/** Effective branding. Public: works on the login page. */
export const getBranding = () => request<Branding>('/api/v1/branding')

export const getAdminBranding = () => request<BrandingAdmin>('/api/v1/admin/branding')

/** Partial update: a string sets the override, null removes it, an absent key is left alone. */
export const updateBranding = (body: BrandingUpdate) =>
  request<BrandingAdmin>('/api/v1/admin/branding', {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })

/** Removes every override and uploaded image. */
export const resetBranding = () => request<BrandingAdmin>('/api/v1/admin/branding', { method: 'DELETE' })

/** Uploads the raw image as the request body (PNG or SVG, at most 512 KiB). */
export const uploadBrandingAsset = (asset: BrandingAsset, file: Blob) =>
  request<BrandingAdmin>(`/api/v1/admin/branding/${asset}`, {
    method: 'PUT',
    headers: { 'Content-Type': file.type },
    body: file,
  })

export const deleteBrandingAsset = (asset: BrandingAsset) =>
  request<BrandingAdmin>(`/api/v1/admin/branding/${asset}`, { method: 'DELETE' })
