import { createElement } from 'react'

// React is provided by the host's import map, never bundled a second time.
export function ExamplePanel() {
  return createElement('p', null, 'Hello from the Nanite example plugin')
}
