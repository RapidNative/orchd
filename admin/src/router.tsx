import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import { RootLayout } from './components/layout'
import { Dashboard } from './routes/dashboard'
import { Projects } from './routes/projects'
import { ProjectDetail } from './routes/project-detail'
import { Backups } from './routes/backups'
import { Activity } from './routes/activity'
import { System } from './routes/system'
import { Settings } from './routes/settings'
import { Templates } from './routes/templates'
import { Images } from './routes/images'

const rootRoute = createRootRoute({ component: RootLayout })


const routeTree = rootRoute.addChildren([
  createRoute({ getParentRoute: () => rootRoute, path: '/', component: Dashboard }),
  createRoute({ getParentRoute: () => rootRoute, path: '/projects', component: Projects }),
  createRoute({ getParentRoute: () => rootRoute, path: '/projects/$id', component: ProjectDetail }),
  createRoute({ getParentRoute: () => rootRoute, path: '/backups', component: Backups }),
  createRoute({ getParentRoute: () => rootRoute, path: '/activity', component: Activity }),
  createRoute({ getParentRoute: () => rootRoute, path: '/templates', component: Templates }),
  createRoute({ getParentRoute: () => rootRoute, path: '/images', component: Images }),
  createRoute({ getParentRoute: () => rootRoute, path: '/system', component: System }),
  createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: Settings }),
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
