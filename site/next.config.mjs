import createMDX from '@next/mdx'
import remarkGfm from 'remark-gfm'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'

// Static export, served from https://rapidnative.github.io/orchd. basePath is
// overridable so a custom domain (or a local preview) can serve it from root:
//   SITE_BASE_PATH= npm run build
const basePath = process.env.SITE_BASE_PATH ?? '/orchd'

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  trailingSlash: true,
  basePath,
  images: { unoptimized: true },
  pageExtensions: ['ts', 'tsx', 'mdx'],
  env: { NEXT_PUBLIC_BASE_PATH: basePath },
}

export default createMDX({
  options: {
    remarkPlugins: [remarkGfm],
    rehypePlugins: [rehypeSlug, [rehypeAutolinkHeadings, { behavior: 'wrap' }]],
  },
})(nextConfig)
