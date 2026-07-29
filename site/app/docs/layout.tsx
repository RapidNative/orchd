import { DocsSidebar } from '@/components/docs-sidebar'
import { PageNav } from '@/components/page-nav'

export default function DocsLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-6xl px-5">
      <div className="flex gap-10">
        <DocsSidebar />
        <main className="min-w-0 flex-1 py-10 lg:py-12">
          <article className="prose">{children}</article>
          <div className="max-w-[46rem]">
            <PageNav />
          </div>
        </main>
      </div>
    </div>
  )
}
