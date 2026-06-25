import type { ArtifactInfo } from './types.js'

export interface ArtifactTreeNode {
  name: string           // segment name (directory or file)
  path: string           // full path to this node
  isDirectory: boolean
  artifact?: ArtifactInfo // only for file nodes
  children: ArtifactTreeNode[]
}

/**
 * Build a tree structure from a flat list of artifacts based on their file paths.
 * Returns a virtual root node whose children are the top-level entries.
 *
 * - Strips common /workspace/ prefix
 * - Collapses single-child directory chains (e.g. output/charts/)
 * - Sorts: directories first (alphabetical), then files (alphabetical)
 */
export function buildArtifactTree(artifacts: ArtifactInfo[]): ArtifactTreeNode {
  const root: ArtifactTreeNode = {
    name: '',
    path: '',
    isDirectory: true,
    children: [],
  }

  for (const artifact of artifacts) {
    // Skip artifacts with missing filePath (e.g. if API normalization failed)
    if (!artifact.filePath) continue
    // Strip leading /workspace/ or workspace/ prefix
    let cleanPath = artifact.filePath.replace(/^\/?(workspace\/)?/, '')
    // Also strip leading slash
    cleanPath = cleanPath.replace(/^\//, '')

    const segments = cleanPath.split('/').filter(Boolean)
    let current = root

    for (let i = 0; i < segments.length; i++) {
      const seg = segments[i]
      const isLast = i === segments.length - 1
      const partialPath = segments.slice(0, i + 1).join('/')

      if (isLast) {
        // File node
        current.children.push({
          name: seg,
          path: partialPath,
          isDirectory: false,
          artifact,
          children: [],
        })
      } else {
        // Directory node — find or create
        let dir = current.children.find(c => c.isDirectory && c.name === seg)
        if (!dir) {
          dir = {
            name: seg,
            path: partialPath,
            isDirectory: true,
            children: [],
          }
          current.children.push(dir)
        }
        current = dir
      }
    }
  }

  // Collapse single-child directory chains
  collapseSingleChildDirs(root)

  // Sort: directories first (alphabetical), then files (alphabetical)
  sortTree(root)

  return root
}

function collapseSingleChildDirs(node: ArtifactTreeNode): void {
  for (const child of node.children) {
    collapseSingleChildDirs(child)
  }

  // Collapse: if this directory has exactly one child and it's a directory, merge them
  for (let i = 0; i < node.children.length; i++) {
    const child = node.children[i]
    if (child.isDirectory && child.children.length === 1 && child.children[0].isDirectory) {
      const grandchild = child.children[0]
      node.children[i] = {
        ...grandchild,
        name: `${child.name}/${grandchild.name}`,
      }
      // Re-process in case there's another layer to collapse
      i--
    }
  }
}

function sortTree(node: ArtifactTreeNode): void {
  node.children.sort((a, b) => {
    // Directories first
    if (a.isDirectory !== b.isDirectory) {
      return a.isDirectory ? -1 : 1
    }
    return a.name.localeCompare(b.name)
  })

  for (const child of node.children) {
    if (child.isDirectory) {
      sortTree(child)
    }
  }
}
