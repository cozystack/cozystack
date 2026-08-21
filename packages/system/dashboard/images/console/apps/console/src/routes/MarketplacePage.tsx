import { Route, Routes } from "react-router"
import { MarketplaceHome } from "./MarketplaceHome.tsx"
import { MarketplaceList } from "./MarketplaceList.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"
import { TapsPage } from "./TapsPage.tsx"

export function MarketplacePage() {
  return (
    <Routes>
      <Route index element={<MarketplaceHome />} />
      <Route path="all" element={<MarketplaceList />} />
      <Route path="c/:category" element={<MarketplaceList />} />
      {/* Static route must precede the :appName catch-all. */}
      <Route path="taps" element={<TapsPage />} />
      <Route path=":appName" element={<ApplicationOrderPage />} />
    </Routes>
  )
}
