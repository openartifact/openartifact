// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn } from 'bun:test'
import { saveBlob } from '../download'

describe('saveBlob', () => {
  afterEach(() => {
    spyOn(URL, 'createObjectURL').mockRestore()
    spyOn(URL, 'revokeObjectURL').mockRestore()
    spyOn(HTMLAnchorElement.prototype, 'click').mockRestore()
  })

  it('keeps the object URL alive after the click and revokes it later', async () => {
    spyOn(URL, 'createObjectURL').mockReturnValue('blob:x')
    const revoke = spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    let name = ''
    spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      name = this.download
    })

    saveBlob(new Blob(['x']), 'k.pem')

    expect(name).toBe('k.pem')
    expect(revoke).not.toHaveBeenCalled()
    await Bun.sleep(1100)
    expect(revoke).toHaveBeenCalledWith('blob:x')
  })
})
