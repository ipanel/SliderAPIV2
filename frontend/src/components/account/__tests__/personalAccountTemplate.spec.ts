import { describe, expect, it } from 'vitest'

import { applyPersonalAccountTemplate, ensureExplicitPersonalAccountModelMapping } from '../personalAccountTemplate'

describe('applyPersonalAccountTemplate', () => {
  it('adds an explicit empty model mapping to a user account payload', () => {
    expect(ensureExplicitPersonalAccountModelMapping({ api_key: 'sk-test' })).toEqual({
      api_key: 'sk-test',
      model_mapping: {}
    })
  })

  it('preserves an explicit empty model mapping', () => {
    const result = applyPersonalAccountTemplate('openai', {
      access_token: 'oauth-token',
      model_mapping: {}
    })

    expect(result.credentials.model_mapping).toEqual({})
  })
})
