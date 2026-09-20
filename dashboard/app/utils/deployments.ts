import type { Deployment, DeploymentKind } from '~/types/api'

/**
 * Valid rollback targets, newest first: the application's own deployments
 * with status SUPERSEDED. That is the agent's rule, mirrored exactly.
 * SUPERSEDED means "served successfully once, replaced since"; the ACTIVE one
 * is what is running now, and FAILED / ROLLED_BACK attempts never served, so
 * none of them is a version to return to.
 */
export function rollbackCandidates<T extends Pick<Deployment, 'id' | 'application' | 'status'>>(
  deployments: readonly T[],
  application: string,
): T[] {
  return deployments
    .filter(d => d.application === application && d.status === 'SUPERSEDED')
    .sort((a, b) => b.id - a.id)
}

export interface DeploymentOrigin {
  kind: Exclude<DeploymentKind, 'deploy'>
  /** "rollback to" / "redeploy of": reads as a phrase in front of the source. */
  phrase: string
  /** Id of the deployment whose stored configuration was re-used; null when the agent recorded none. */
  sourceId: number | null
  /** The source itself, when it is among the deployments we already hold. */
  source: Pick<Deployment, 'id' | 'sequence' | 'version'> | null
}

/**
 * Where a deployment's configuration came from, for history rows and the
 * detail page: "rollback to #3 (1.4.1)", "redeploy of #6". Null for a plain
 * deploy.
 */
export function deploymentOrigin(
  deployment: Pick<Deployment, 'kind' | 'source_deployment_id'>,
  known: readonly Pick<Deployment, 'id' | 'sequence' | 'version'>[] = [],
): DeploymentOrigin | null {
  if (deployment.kind !== 'rollback' && deployment.kind !== 'redeploy') return null
  const sourceId = deployment.source_deployment_id
  return {
    kind: deployment.kind,
    phrase: deployment.kind === 'rollback' ? 'rollback to' : 'redeploy of',
    sourceId,
    source: sourceId === null ? null : known.find(d => d.id === sourceId) ?? null,
  }
}

/** Plain-text form of an origin: "rollback to #3 (1.4.1)", or just "rollback" when the source is unknown. */
export function formatOrigin(origin: DeploymentOrigin): string {
  if (!origin.source) return origin.kind
  const version = origin.source.version ? ` (${origin.source.version})` : ''
  return `${origin.phrase} #${origin.source.sequence}${version}`
}
