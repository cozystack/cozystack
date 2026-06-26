import { Route, Routes } from "react-router"
import { MarketplaceHome } from "./MarketplaceHome.tsx"
import { MarketplaceList } from "./MarketplaceList.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"

export function MarketplacePage() {
  return (
    <Routes>
      <Route index element={<MarketplaceHome />} />
      <Route path="all" element={<MarketplaceList />} />
      <Route path="c/:category" element={<MarketplaceList />} />
      <Route path=":appName" element={<ApplicationOrderPage />} />
    </Routes>
  )
}
