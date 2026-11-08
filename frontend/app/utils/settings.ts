import type { NodeConnectionRequest } from '../types'

// IDs stay in routes and stored secret flags stay local for both adding and editing nodes.
export const nodeConnectionRequest = (node: NodeConnectionRequest): NodeConnectionRequest => ({
  name: node.name.trim(),
  url: node.url.trim(),
  token: node.token,
  fingerprint: node.fingerprint.trim(),
  public_host: node.public_host.trim(),
})
