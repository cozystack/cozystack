import { Route, Routes } from "react-router"
import { MarketplaceList } from "./MarketplaceList.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"
import { TapsPage } from "./TapsPage.tsx"

export function MarketplacePage() {
  return (
    <Routes>
      <Route index element={<MarketplaceList />} />
      <Route path="c/:category" element={<MarketplaceList />} />
      {/* Static route must precede the :appName catch-all. */}
      <Route path="taps" element={<TapsPage />} />
      <Route path=":appName" element={<ApplicationOrderPage />} />
    </Routes>
  )
}
