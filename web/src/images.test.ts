// B4: the pure half of the image catalogue — coercion of the wire shape and
// the picker's option list.

import { describe, it, expect } from 'vitest'
import { coerceImage, imageOptionsFrom, IMAGE_ENDPOINTS, type ProjectImage } from './images.js'

const img = (extra: Partial<ProjectImage> = {}): ProjectImage => ({
  name: 'toolbox',
  version: 1,
  labels: {},
  created_by_worker: '',
  created_by_session: '',
  created_at: 0,
  ...extra,
})

describe('coerceImage', () => {
  const cases: { name: string; raw: unknown; want: ProjectImage }[] = [
    {
      name: 'a full record survives verbatim',
      raw: {
        name: 'toolbox',
        version: 3,
        labels: { purpose: 'marketing' },
        created_by_worker: 'burner',
        created_by_session: 'sess-2',
        created_at: 1789000002,
      },
      want: img({
        version: 3,
        labels: { purpose: 'marketing' },
        created_by_worker: 'burner',
        created_by_session: 'sess-2',
        created_at: 1789000002,
      }),
    },
    {
      name: 'missing fields fall back rather than throwing',
      raw: { name: 'toolbox' },
      want: img({ version: 0 }),
    },
    {
      name: 'non-string labels are dropped, never stringified',
      raw: { name: 'toolbox', labels: { ok: 'yes', bad: { nested: true }, n: 3 } },
      want: img({ version: 0, labels: { ok: 'yes' } }),
    },
    { name: 'junk is a blank record', raw: 'nope', want: img({ name: '', version: 0 }) },
    { name: 'null is a blank record', raw: null, want: img({ name: '', version: 0 }) },
  ]
  for (const c of cases) {
    it(c.name, () => {
      expect(coerceImage(c.raw)).toEqual(c.want)
    })
  }
})

describe('imageOptionsFrom', () => {
  it('offers each name once, in the catalogue order (newest first)', () => {
    const options = imageOptionsFrom([
      img({ name: 'toolbox', version: 3 }),
      img({ name: 'renderer', version: 1 }),
      img({ name: 'toolbox', version: 2 }),
      img({ name: 'toolbox', version: 1 }),
    ])
    expect(options).toEqual(['toolbox', 'renderer'])
  })

  it('offers bare names, never pins — a bare name resolves to the newest burn', () => {
    expect(imageOptionsFrom([img({ name: 'toolbox', version: 7 })])).toEqual(['toolbox'])
  })

  it('drops nameless rows and an empty catalogue is an empty list', () => {
    expect(imageOptionsFrom([img({ name: '' })])).toEqual([])
    expect(imageOptionsFrom([])).toEqual([])
  })
})

describe('IMAGE_ENDPOINTS', () => {
  it('points at the read-only catalogue route', () => {
    expect(IMAGE_ENDPOINTS.list).toBe('/agent/images')
  })
})
