package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun AegisOpsDashboardScreen(
    onBackClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val darkBg = Color(0xFF0F172A)
    val cardBg = Color(0xFF1E293B)
    val textLight = Color(0xFFF8FAFC)
    val emeraldGreen = Color(0xFF10B981)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Aegis Ops Dashboard", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, color = textLight) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = textLight)
                    }
                },
                actions = {
                    OutlinedButton(onClick = {}, shape = RoundedCornerShape(8.dp), modifier = Modifier.padding(end = 8.dp)) {
                        Icon(Icons.Default.FileDownload, contentDescription = null, tint = textLight, modifier = Modifier.size(16.dp))
                        Spacer(modifier = Modifier.width(4.dp))
                        Text("Export Report", fontSize = 11.sp, color = textLight)
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = darkBg)
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(darkBg)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Environment Indicator
            item {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Surface(shape = RoundedCornerShape(4.dp), color = emeraldGreen) {
                        Box(modifier = Modifier.size(8.dp).padding(2.dp))
                    }
                    Text("Environment: Production", fontSize = 12.sp, fontFamily = FontFamily.Monospace, color = Color.LightGray)
                }
            }

            // Top 4 Metrics Row (2x2)
            item {
                Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        OpsMetricCard("TOTAL AI REQUESTS", "1.2M", "+12%", Color(0xFF38BDF8), Modifier.weight(1f))
                        OpsMetricCard("GLOBAL LATENCY", "185 ms", "-5ms", emeraldGreen, Modifier.weight(1f))
                    }
                    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                        OpsMetricCard("ERROR RATE", "0.04%", "Healthy", emeraldGreen, Modifier.weight(1f))
                        OpsMetricCard("ACTIVE CONVERSATIONS", "4,280", "Live", Color(0xFFA855F7), Modifier.weight(1f))
                    }
                }
            }

            // Regional Node Health Card
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Icons.Default.Public, contentDescription = null, tint = Color(0xFF38BDF8))
                                Text("Regional Node Health", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = textLight)
                            }
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Text("● Optimal", fontSize = 10.sp, color = emeraldGreen)
                                Text("● Degraded", fontSize = 10.sp, color = Color(0xFFF59E0B))
                            }
                        }

                        Surface(shape = RoundedCornerShape(12.dp), color = Color(0xFF0284C7).copy(alpha = 0.15f), modifier = Modifier.fillMaxWidth().height(120.dp)) {
                            Box(contentAlignment = Alignment.Center) {
                                Text("Global Data Flow Map (AWS ap-south-1 Mumbai / EU-Central)", fontSize = 11.sp, fontFamily = FontFamily.Monospace, color = textLight)
                            }
                        }
                    }
                }
            }

            // Recent Incidents Log
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Text("Recent Incidents", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = textLight)
                            Text("View All", fontSize = 11.sp, color = Color(0xFF818CF8))
                        }

                        IncidentRow("Carrier API Timeout", "Sev-1", "ID: INC-9042 • EU-Central • 14:32 UTC", "Resolved", emeraldGreen)
                        IncidentRow("Database Sync Lag", "Sev-3", "ID: INC-9043 • US-East • 15:10 UTC", "Investigating", Color(0xFFF59E0B))
                    }
                }
            }
        }
    }
}

@Composable
private fun OpsMetricCard(title: String, value: String, delta: String, deltaColor: Color, modifier: Modifier = Modifier) {
    Card(
        shape = RoundedCornerShape(12.dp),
        colors = CardDefaults.cardColors(containerColor = Color(0xFF1E293B)),
        modifier = modifier
    ) {
        Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Text(title, fontSize = 10.sp, color = Color.Gray, fontFamily = FontFamily.Monospace)
            Text(value, fontWeight = FontWeight.ExtraBold, fontSize = 20.sp, color = Color.White)
            Text(delta, fontSize = 10.sp, color = deltaColor, fontWeight = FontWeight.Bold)
        }
    }
}

@Composable
private fun IncidentRow(title: String, sev: String, meta: String, status: String, statusColor: Color) {
    Row(
        modifier = Modifier.fillMaxWidth().background(Color(0xFF334155).copy(alpha = 0.5f), RoundedCornerShape(8.dp)).padding(10.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(title, fontWeight = FontWeight.Bold, fontSize = 12.sp, color = Color.White)
                Surface(shape = RoundedCornerShape(4.dp), color = Color.DarkGray) {
                    Text(sev, modifier = Modifier.padding(horizontal = 4.dp, vertical = 2.dp), fontSize = 9.sp, color = Color.White)
                }
            }
            Text(meta, fontSize = 10.sp, fontFamily = FontFamily.Monospace, color = Color.LightGray)
        }
        Surface(shape = RoundedCornerShape(6.dp), color = statusColor.copy(alpha = 0.2f)) {
            Text(status, modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold, color = statusColor)
        }
    }
}
