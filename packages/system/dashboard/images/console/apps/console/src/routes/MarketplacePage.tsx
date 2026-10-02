import { Navigate, Route, Routes } from "react-router"
import { MarketplaceList } from "./MarketplaceList.tsx"
import { ApplicationOrderPage } from "./ApplicationOrderPage.tsx"

export function MarketplacePage() {
  return (
    <Routes>
      <Route index element={<MarketplaceList />} />
      <Route path="c/:category" element={<MarketplaceList />} />
      {/* Repositories moved to Admin; must precede the :appName catch-all. */}
      <Route path="taps" element={<Navigate to="/admin/taps" replace />} />
      <Route path=":appName" element={<ApplicationOrderPage />} />
    </Routes>
  )
}
