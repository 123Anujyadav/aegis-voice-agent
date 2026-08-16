package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

import androidx.compose.material.icons.automirrored.filled.ArrowBack

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun CallRoutingBuilderScreen(
    onBackClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val darkContainerColor = Color(0xFF0F172A)
    val cardBg = Color(0xFF1E293B)
    val textLight = Color(0xFFF8FAFC)
    val accentPurple = Color(0xFF818CF8)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Model Routing & Traffic Split", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, color = textLight) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = textLight)
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = darkContainerColor)
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(darkContainerColor)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Model Traffic Split Node Box
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Icons.Default.AccountTree, contentDescription = null, tint = accentPurple)
                                Text("Model Routing & Traffic Split", fontWeight = FontWeight.Bold, fontSize = 15.sp, color = textLight)
                            }
                            OutlinedButton(onClick = {}, shape = RoundedCornerShape(8.dp)) {
                                Text("Configure", fontSize = 11.sp, color = accentPurple)
                            }
                        }

                        // Traffic Nodes Flow
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            // Inbound Node
                            Card(
                                shape = RoundedCornerShape(12.dp),
                                colors = CardDefaults.cardColors(containerColor = Color(0xFF334155)),
                                modifier = Modifier.weight(1f)
                            ) {
                                Column(modifier = Modifier.padding(12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                                    Text("Inbound API", fontSize = 11.sp, color = Color.LightGray)
                                    Text("14.2k", fontWeight = FontWeight.ExtraBold, fontSize = 22.sp, color = textLight)
                                    Text("req/min", fontSize = 10.sp, color = Color.Gray)
                                }
                            }

                            Spacer(modifier = Modifier.width(12.dp))

                            // Split Targets
                            Column(modifier = Modifier.weight(2f), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                RouteTargetCard("Gemini 1.5 Pro", "Primary Router", "70%", Color(0xFF4F46E5))
                                RouteTargetCard("Gemini Flash", "Low Latency", "25%", Color(0xFF6366F1))
                                RouteTargetCard("Claude 3 Haiku", "Fallback", "5%", Color(0xFF475569))
                            }
                        }
                    }
                }
            }

            // Live Performance & Metrics
            item {
                Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = cardBg),
                        modifier = Modifier.weight(1f)
                    ) {
                        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text("Quality Score (Eval)", fontSize = 12.sp, color = Color.LightGray)
                            Text("8.9 /10", fontWeight = FontWeight.Bold, fontSize = 20.sp, color = textLight)
                            LinearProgressIndicator(progress = { 0.89f }, modifier = Modifier.fillMaxWidth(), color = Color(0xFF10B981))
                        }
                    }

                    Card(
                        shape = RoundedCornerShape(16.dp),
                        colors = CardDefaults.cardColors(containerColor = cardBg),
                        modifier = Modifier.weight(1f)
                    ) {
                        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text("Hallucination Rate", fontSize = 12.sp, color = Color.LightGray)
                            Text("0.2 %", fontWeight = FontWeight.Bold, fontSize = 20.sp, color = textLight)
                            LinearProgressIndicator(progress = { 0.02f }, modifier = Modifier.fillMaxWidth(), color = Color(0xFFF59E0B))
                        }
                    }
                }
            }


            // Cost & Token Monitor
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Text("Cost & Token Monitor", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = textLight)
                            Text("Billing Cycle: Oct", fontSize = 12.sp, color = Color.Gray)
                        }

                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Column {
                                Text("Tokens / Min (Avg)", fontSize = 11.sp, color = Color.LightGray)
                                Text("1.2M", fontWeight = FontWeight.Bold, fontSize = 22.sp, color = textLight)
                            }

                            Column(horizontalAlignment = Alignment.End) {
                                Text("Est. Monthly Spend", fontSize = 11.sp, color = Color.LightGray)
                                Text("$4,850", fontWeight = FontWeight.Bold, fontSize = 22.sp, color = Color(0xFF10B981))
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun RouteTargetCard(name: String, role: String, percent: String, color: Color) {
    Card(
        shape = RoundedCornerShape(8.dp),
        colors = CardDefaults.cardColors(containerColor = color),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp).fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column {
                Text(name, fontWeight = FontWeight.Bold, fontSize = 12.sp, color = Color.White)
                Text(role, fontSize = 10.sp, color = Color.White.copy(alpha = 0.8f))
            }
            Text(percent, fontWeight = FontWeight.ExtraBold, fontSize = 14.sp, color = Color.White)
        }
    }
}
