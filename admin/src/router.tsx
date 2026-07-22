import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import { RootLayout } from './components/layout'
import { Dashboard } from './routes/dashboard'
import { Projects } from './routes/projects'
import { ProjectDetail } from './routes/project-detail'
import { Backups } from './routes/backups'
import { Activity } from './routes/activity'
import { System } from './routes/system'
import { Settings } from './routes/settings'
import { Images } from './routes/images'
import { DocsLayout } from './routes/docs/layout'
import { About, Adaptors, ImagesDoc, Regions, Repo, Templates } from './routes/docs/sections'
import { ApiReference } from './routes/docs/api'

const rootRoute = createRootRoute({ component: RootLayout })

const docsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/docs',
  component: DocsLayout,
})
const docsRoutes = docsRoute.addChildren([
  createRoute({ getParentRoute: () => docsRoute, path: '/', component: About }),
  createRoute({ getParentRoute: () => docsRoute, path: 'repo', component: Repo }),
  createRoute({ getParentRoute: () => docsRoute, path: 'templates', component: Templates }),
  createRoute({ getParentRoute: () => docsRoute, path: 'images', component: ImagesDoc }),
  createRoute({ getParentRoute: () => docsRoute, path: 'regions', component: Regions }),
  createRoute({ getParentRoute: () => docsRoute, path: 'adaptors', component: Adaptors }),
  createRoute({ getParentRoute: () => docsRoute, path: 'api', component: ApiReference }),
])

const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: '/', component: Dashboard }),
  createRoute({ getParentRoute: () => rootRoute, path: '/projects', component: Projects }),
  createRoute({ getParentRoute: () => rootRoute, path: '/projects/$id', component: ProjectDetail }),
  createRoute({ getParentRoute: () => rootRoute, path: '/backups', component: Backups }),
  createRoute({ getParentRoute: () => rootRoute, path: '/activity', component: Activity }),
  createRoute({ getParentRoute: () => rootRoute, path: '/images', component: Images }),
  createRoute({ getParentRoute: () => rootRoute, path: '/system', component: System }),
  createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: Settings }),
  docsRoutes,
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
