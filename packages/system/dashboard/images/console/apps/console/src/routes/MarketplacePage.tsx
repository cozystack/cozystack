import { Route, Routes } from "react-router"
import { MarketplaceList } from "./MarketplaceList.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"

export function MarketplacePage() {
  return (
    <Routes>
      <Route index element={<MarketplaceList />} />
      <Route path="c/:category" element={<MarketplaceList />} />
      <Route path=":appName" element={<ApplicationOrderPage />} />
    </Routes>
  )
}
